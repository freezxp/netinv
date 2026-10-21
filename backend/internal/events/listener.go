package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// MetricName is the series each trap from a known device writes, value 1 at
// the moment of receipt. Rules alert on it with max_over_time over a window,
// so a trap alert fires on arrival and resolves once the window passes with no
// repeat — a trap is an event, and has no "cleared" state of its own (doc 36 §4).
const MetricName = "netinv_snmp_trap"

// Store persists decoded traps.
type Store interface {
	InsertTraps(ctx context.Context, traps []*Trap) error
}

// Publisher hands metric samples to the ingester (metrics.raw), which adds the
// device/site/vendor labels every other series carries.
type Publisher interface {
	PublishSamples(ctx context.Context, samples []wire.Sample) error
}

// Resolver maps a source address to the device that owns it.
type Resolver interface {
	DeviceByAddr(a netip.Addr) string
}

type Listener struct {
	Addr        string
	Allow       []netip.Prefix  // empty = any source
	Communities map[string]bool // empty = any community
	Resolve     Resolver
	Store       Store
	Publish     Publisher
	Log         *slog.Logger

	// PerSourceRate and PerSourceBurst bound how fast one address can write.
	// A link flapping on a large switch produces a legitimate burst; a looping
	// or hostile sender produces an endless one, and without a bound it writes
	// a row per datagram for as long as it keeps going.
	PerSourceRate  float64
	PerSourceBurst float64

	// MaxTrapLabelsPerDevice caps distinct trap labels per device in the
	// metric; past it, a trap is labelled "other". Each distinct label is a
	// series, and the label is chosen by whoever sends the datagram.
	MaxTrapLabelsPerDevice int

	FlushEvery time.Duration

	queue   chan *Trap
	limiter *limiter
	labels  labelCap
	stats   counters
}

type counters struct {
	received, stored, refused, badCommunity, unsupported, malformed,
	rateLimited, queueFull, unknownSource, informsAcked atomic.Uint64
}

func (l *Listener) defaults() {
	if l.PerSourceRate <= 0 {
		l.PerSourceRate = 20
	}
	if l.PerSourceBurst <= 0 {
		l.PerSourceBurst = 200
	}
	if l.MaxTrapLabelsPerDevice <= 0 {
		l.MaxTrapLabelsPerDevice = 100
	}
	if l.FlushEvery <= 0 {
		l.FlushEvery = time.Second
	}
	l.queue = make(chan *Trap, 10000)
	l.limiter = newLimiter(l.PerSourceRate, l.PerSourceBurst)
	l.labels = labelCap{max: l.MaxTrapLabelsPerDevice, seen: map[string]map[string]bool{}}
}

// Run binds, then serves until ctx is cancelled. Every configured address must
// bind or Run fails: a listener deaf on one port while reporting healthy is the
// failure flow collection already learned to refuse (ADR-022).
func (l *Listener) Run(ctx context.Context) error {
	l.defaults()
	var conns []net.PacketConn
	for _, a := range strings.Split(l.Addr, ",") {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		pc, err := net.ListenPacket("udp", a)
		if err != nil {
			for _, c := range conns {
				c.Close()
			}
			return fmt.Errorf("events: listen %s: %w", a, err)
		}
		conns = append(conns, pc)
	}
	if len(conns) == 0 {
		return fmt.Errorf("events: no listen address configured")
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	l.Log.Info("snmp trap listener", "addr", l.Addr, "allow", len(l.Allow),
		"communities", len(l.Communities))

	done := make(chan struct{})
	go func() { l.writeLoop(ctx); close(done) }()
	go l.statsLoop(ctx)

	var wg sync.WaitGroup
	for _, pc := range conns {
		wg.Add(1)
		go func(pc net.PacketConn) { defer wg.Done(); l.serve(ctx, pc) }(pc)
	}
	wg.Wait()
	<-done
	return nil
}

func (l *Listener) serve(ctx context.Context, pc net.PacketConn) {
	buf := make([]byte, 65535)
	for ctx.Err() == nil {
		_ = pc.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if ctx.Err() == nil {
				l.Log.Warn("trap read failed", "err", err)
			}
			continue
		}
		if ack := l.Handle(buf[:n], addr, time.Now().UTC()); ack != nil {
			if _, err := pc.WriteTo(ack, addr); err == nil {
				l.stats.informsAcked.Add(1)
			}
		}
	}
}

// Handle processes one datagram and returns the bytes to send back (an inform
// acknowledgement) or nil. Exported so tests can drive it without a socket.
func (l *Listener) Handle(b []byte, from net.Addr, at time.Time) []byte {
	l.stats.received.Add(1)
	src := addrOf(from)
	if !l.allowed(src) {
		l.stats.refused.Add(1)
		return nil
	}
	t, community, err := Decode(b, src, at)
	switch {
	case errors.Is(err, ErrUnsupportedVersion):
		l.stats.unsupported.Add(1)
		return nil
	case err != nil:
		l.stats.malformed.Add(1)
		return nil
	}
	// The community is the only authentication v1/v2c offer. It is compared
	// and never stored: in a trap it is the sender's secret.
	if len(l.Communities) > 0 && !l.Communities[community] {
		l.stats.badCommunity.Add(1)
		return nil
	}
	if !l.limiter.allow(src, at) {
		l.stats.rateLimited.Add(1)
		return nil
	}
	if l.Resolve != nil {
		t.DeviceID = l.Resolve.DeviceByAddr(src)
		if t.DeviceID == "" && t.Agent.IsValid() {
			t.DeviceID = l.Resolve.DeviceByAddr(t.Agent)
		}
	}
	if t.DeviceID == "" {
		l.stats.unknownSource.Add(1)
	}
	// Acknowledge before queuing: the agent's retry timer is short, and a full
	// queue is our problem, not a reason for it to resend.
	ack, _ := t.Ack()
	select {
	case l.queue <- t:
	default:
		// Never block the socket on storage. A dropped trap is counted and
		// logged; a blocked reader drops everything behind it silently.
		l.stats.queueFull.Add(1)
	}
	return ack
}

func (l *Listener) allowed(a netip.Addr) bool {
	if len(l.Allow) == 0 {
		return true
	}
	for _, p := range l.Allow {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (l *Listener) writeLoop(ctx context.Context) {
	tick := time.NewTicker(l.FlushEvery)
	defer tick.Stop()
	var batch []*Trap
	flush := func(ctx context.Context) {
		if len(batch) == 0 {
			return
		}
		l.write(ctx, batch)
		batch = batch[:0]
	}
	for {
		select {
		case t := <-l.queue:
			batch = append(batch, t)
			if len(batch) >= 500 {
				flush(ctx)
			}
		case <-tick.C:
			flush(ctx)
		case <-ctx.Done():
			// Drain what is already queued so a restart does not lose the
			// traps that arrived just before it.
			for {
				select {
				case t := <-l.queue:
					batch = append(batch, t)
				default:
					flush(context.WithoutCancel(ctx))
					return
				}
			}
		}
	}
}

func (l *Listener) write(ctx context.Context, batch []*Trap) {
	if l.Store != nil {
		if err := l.Store.InsertTraps(ctx, batch); err != nil {
			l.Log.Error("trap store failed", "traps", len(batch), "err", err)
		} else {
			l.stats.stored.Add(uint64(len(batch)))
		}
	}
	if l.Publish == nil {
		return
	}
	var samples []wire.Sample
	for _, t := range batch {
		// Only known devices write series. An unknown source still lands in the
		// event store, where an operator can see it and add the device; giving it
		// a series would let any address that passes the allow-list mint one.
		if t.DeviceID == "" {
			continue
		}
		labels := map[string]string{"trap": l.labels.label(t.DeviceID, t.Label())}
		if t.IfIndex != "" {
			labels["if_index"] = t.IfIndex
		}
		samples = append(samples, wire.Sample{
			DeviceID: t.DeviceID, Name: MetricName, Labels: labels,
			TSMillis: t.At.UnixMilli(), Value: 1,
		})
	}
	if len(samples) == 0 {
		return
	}
	if err := l.Publish.PublishSamples(ctx, samples); err != nil {
		l.Log.Error("trap metric publish failed", "samples", len(samples), "err", err)
	}
}

// statsLoop logs intake once a minute, and only when something arrived — the
// same convention as the flow collector, so silence in the log means silence
// on the wire.
func (l *Listener) statsLoop(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	var prev [10]uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s := &l.stats
		cur := [10]uint64{s.received.Load(), s.stored.Load(), s.refused.Load(),
			s.badCommunity.Load(), s.unsupported.Load(), s.malformed.Load(),
			s.rateLimited.Load(), s.queueFull.Load(), s.unknownSource.Load(),
			s.informsAcked.Load()}
		if cur[0] == prev[0] {
			continue
		}
		d := func(i int) uint64 { return cur[i] - prev[i] }
		lvl := slog.LevelInfo
		if d(7) > 0 {
			lvl = slog.LevelWarn
		}
		l.Log.Log(ctx, lvl, "trap intake",
			"received", d(0), "stored", d(1), "refused_source", d(2),
			"bad_community", d(3), "unsupported_v3", d(4), "malformed", d(5),
			"rate_limited", d(6), "queue_full", d(7), "unknown_source", d(8),
			"informs_acked", d(9))
		prev = cur
		l.limiter.sweep(time.Now())
	}
}

func addrOf(a net.Addr) netip.Addr {
	if u, ok := a.(*net.UDPAddr); ok {
		if ip, ok := netip.AddrFromSlice(u.IP); ok {
			return ip.Unmap()
		}
	}
	if ap, err := netip.ParseAddrPort(a.String()); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}

// ParseAllow parses a comma-separated list of prefixes or bare addresses.
func ParseAllow(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			a, err := netip.ParseAddr(part)
			if err != nil {
				return nil, fmt.Errorf("allow %q: %w", part, err)
			}
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("allow %q: %w", part, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// limiter is a token bucket per source address.
type limiter struct {
	mu          sync.Mutex
	rate, burst float64
	buckets     map[netip.Addr]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate, burst float64) *limiter {
	return &limiter{rate: rate, burst: burst, buckets: map[netip.Addr]*bucket{}}
}

func (l *limiter) allow(a netip.Addr, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[a]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[a] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep forgets sources idle long enough to have refilled, so the map is
// bounded by recently active senders rather than by every address ever seen.
func (l *limiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	idle := time.Duration(l.burst/l.rate*float64(time.Second)) + time.Minute
	for a, b := range l.buckets {
		if now.Sub(b.last) > idle {
			delete(l.buckets, a)
		}
	}
}

type labelCap struct {
	mu   sync.Mutex
	max  int
	seen map[string]map[string]bool
}

func (c *labelCap) label(device, trap string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.seen[device]
	if m == nil {
		m = map[string]bool{}
		c.seen[device] = m
	}
	if m[trap] {
		return trap
	}
	if len(m) >= c.max {
		return "other"
	}
	m[trap] = true
	return trap
}
