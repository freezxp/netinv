package flow

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync/atomic"
	"time"
)

// Writer publishes drained buckets. Implemented against VictoriaMetrics; an
// interface so the collector can be tested without one.
type Writer interface {
	WriteFlow(ctx context.Context, at time.Time, buckets []Bucket) error
}

// Collector listens for flow exports and publishes aggregates.
//
// This is the first NetInv component that accepts unsolicited input from the
// network — everything else initiates its own connections. Two consequences
// are built in rather than left to deployment: an optional source allow-list,
// and a hard bound on how much state one interval can accumulate.
type Collector struct {
	Addr  string // e.g. ":2055"
	Agg   *Aggregator
	Write Writer
	Log   *slog.Logger

	// Allow restricts which sources are accepted. Empty means accept any,
	// which is the sane default on a management network and the wrong one on
	// an untrusted segment — doc 34 §6.
	Allow []netip.Prefix

	// Interval between drains. Zero means Interval.
	Every time.Duration

	// Counters are atomic: the receive loop writes them while drainLoop reads
	// them on the ticker goroutine. Plain uint64s here were a real data race,
	// caught by -race once the receive path had a test that ran it.
	stats struct {
		packets, records, refused, malformed atomic.Uint64
	}
	// prev holds the totals as of the last drain, so intake can be reported as
	// a per-interval delta. Owned by drainLoop alone.
	prev struct {
		packets, records, refused, malformed uint64
	}
}

// Run listens until the context is cancelled.
func (c *Collector) Run(ctx context.Context) error {
	if c.Agg == nil {
		c.Agg = NewAggregator()
	}
	every := c.Every
	if every <= 0 {
		every = Interval
	}

	pc, err := net.ListenPacket("udp", c.Addr)
	if err != nil {
		return fmt.Errorf("flow: listen %s: %w", c.Addr, err)
	}
	defer pc.Close()
	c.Log.Info("flow collector listening", "addr", c.Addr,
		"allow", len(c.Allow), "interval", every.String())

	go c.drainLoop(ctx, every)

	// One datagram is at most 64 KB; flow exports are far smaller, but a
	// short buffer would silently truncate a packet into a decode error.
	buf := make([]byte, 65535)
	for {
		if ctx.Err() != nil {
			return nil
		}
		// A deadline rather than a blocking read, so cancellation is noticed
		// on a quiet network instead of at the next packet — which on a fleet
		// with no exporter is never.
		_ = pc.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			c.Log.Warn("flow read failed", "err", err)
			continue
		}
		c.handle(buf[:n], addr)
	}
}

func (c *Collector) handle(b []byte, addr net.Addr) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return
	}
	from, ok := netip.AddrFromSlice(ua.IP)
	if !ok {
		return
	}
	from = from.Unmap()
	if !c.allowed(from) {
		c.stats.refused.Add(1)
		return
	}
	version, ok := Version(b)
	if !ok {
		c.stats.malformed.Add(1)
		return
	}
	switch version {
	case 5:
		p, err := DecodeNetFlowV5(b, from)
		if err != nil {
			c.stats.malformed.Add(1)
			// Debug, not warn: a malformed datagram from the network is not an
			// operator's problem to read about every second, and at warn a
			// single misconfigured exporter would drown the log.
			c.Log.Debug("flow decode failed", "from", from.String(), "err", err)
			return
		}
		c.stats.packets.Add(1)
		c.stats.records.Add(uint64(len(p.Records)))
		c.Agg.Add(p)
	default:
		// v9, IPFIX (10) and sFlow (datagram version 5 on its own port) are
		// not decoded yet. Counted rather than logged so an operator can see
		// from /metrics that something is arriving and being ignored.
		c.stats.malformed.Add(1)
	}
}

func (c *Collector) allowed(a netip.Addr) bool {
	if len(c.Allow) == 0 {
		return true
	}
	for _, p := range c.Allow {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (c *Collector) drainLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			// Report what arrived even when nothing was usable. A collector
			// that is receiving packets it cannot decode looks identical to
			// one receiving nothing at all, and the difference — a
			// misconfigured exporter versus none — is the first thing an
			// operator needs. Debug-only logging hid exactly that during
			// development.
			//
			// Deltas, not the running totals: these lines describe the interval
			// that just closed. Logging cumulative counters would re-report one
			// bad packet every minute for the life of the process, and would
			// attribute packets to an interval that drained cleanly minutes ago.
			pkts, recs, refused, malformed := c.interval()
			if refused > 0 || malformed > 0 {
				c.Log.Info("flow intake", "packets", pkts, "records", recs,
					"refused_by_allowlist", refused, "undecodable", malformed)
			}

			dropped := c.Agg.Dropped() // Drain resets it, so sample it first
			buckets := c.Agg.Drain()
			if len(buckets) == 0 {
				if pkts == 0 && malformed == 0 && refused == 0 {
					continue // genuinely idle: no exporter is sending anything
				}
				if pkts == 0 {
					continue // already reported above as refused or undecodable
				}
				c.Log.Info("flow received but nothing aggregated",
					"packets", pkts, "undecodable", malformed,
					"refused_by_allowlist", refused,
					"hint", "flows with no ingress or egress ifIndex cannot be attributed")
				continue
			}
			// Timestamp the interval's end. Flow describes a window, and
			// stamping it "now" is the closest honest choice without
			// pretending to a precision the export does not carry.
			if err := c.Write.WriteFlow(ctx, now.UTC(), buckets); err != nil {
				c.Log.Warn("flow write failed", "buckets", len(buckets), "err", err)
				continue
			}
			if dropped > 0 {
				// Worth an operator's attention: the top-N tables are still
				// correct, but the "other" bucket is now hiding detail that the
				// key cap refused to keep.
				c.Log.Warn("flow key cap reached — detail folded into \"other\"",
					"records", dropped, "max_keys", c.Agg.MaxKeys)
			}
			c.Log.Debug("flow aggregates written", "buckets", len(buckets))
		}
	}
}

// interval returns what arrived since the last call, for reporting on the
// interval that just closed. Only drainLoop calls it, so the previous-values
// state needs no lock of its own.
func (c *Collector) interval() (packets, records, refused, malformed uint64) {
	p, r, rf, m := c.Stats()
	packets, records = p-c.prev.packets, r-c.prev.records
	refused, malformed = rf-c.prev.refused, m-c.prev.malformed
	c.prev.packets, c.prev.records = p, r
	c.prev.refused, c.prev.malformed = rf, m
	return packets, records, refused, malformed
}

// Stats reports the running totals since start.
func (c *Collector) Stats() (packets, records, refused, malformed uint64) {
	return c.stats.packets.Load(), c.stats.records.Load(),
		c.stats.refused.Load(), c.stats.malformed.Load()
}

// ParseAllow turns a comma-separated list of CIDRs or bare addresses into
// prefixes. A bare address becomes a /32 or /128, which is what an operator
// listing one exporter means.
func ParseAllow(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range splitComma(s) {
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p)
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("flow: %q is neither an address nor a CIDR", part)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		switch r {
		case ',':
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
		case ' ', '\t':
		default:
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
