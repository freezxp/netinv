package flowstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/freezxp/netinv/backend/internal/flow"
	"github.com/freezxp/netinv/backend/internal/flow/geo"
)

// Writer queues decoded flows and inserts them in batches. It implements the
// collector's raw sink.
//
// It never blocks the receive loop. A full queue drops the newest flows and
// counts them; a failed insert keeps its batch and retries it, with the same
// deduplication token, until it lands or is pushed out by the queue bound.
// Raw storage is best-effort by design (ADR-026): the aggregates in
// VictoriaMetrics do not depend on any of this.
type Writer struct {
	C   *Client
	Log *slog.Logger
	// MaxQueued bounds the rows held in memory, across pending batches.
	// Zero means DefaultMaxQueued.
	MaxQueued int
	// Every is the flush interval. Zero means one second.
	Every time.Duration

	geo atomic.Pointer[geo.DB]

	mu      sync.Mutex
	pending []row
	// batches already formed but not yet accepted by ClickHouse, oldest
	// first; each keeps the token it was first sent with.
	retry  []batch
	queued int
	seq    uint64

	written, dropped, failed atomic.Uint64
}

// DefaultMaxQueued is about two minutes of a busy 10 000 flows/s exporter at
// roughly 400 bytes per queued row — some 500 MB at the worst, which is the
// price of riding out a ClickHouse restart without losing the flows.
const DefaultMaxQueued = 1_000_000

// batchRows caps one INSERT. ClickHouse prefers few large inserts to many
// small ones; one second of flow is normally well under this.
const batchRows = 100_000

type batch struct {
	token string
	rows  []row
}

// SetGeo swaps in a (re)loaded enrichment database. Nil disables enrichment.
func (w *Writer) SetGeo(db *geo.DB) { w.geo.Store(db) }

// Geo returns the database in use, for status reporting.
func (w *Writer) Geo() *geo.DB { return w.geo.Load() }

// Add queues a packet's records. Safe for concurrent use; returns at once.
func (w *Writer) Add(p *flow.Packet) {
	if p == nil || len(p.Records) == 0 {
		return
	}
	now := time.Now().UTC()
	g := w.geo.Load()
	rows := make([]row, 0, len(p.Records))
	for i := range p.Records {
		rows = append(rows, toRow(now, p.ExporterIP, &p.Records[i], g))
	}
	max := w.MaxQueued
	if max <= 0 {
		max = DefaultMaxQueued
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := max - w.queued; room < len(rows) {
		if room < 0 {
			room = 0
		}
		w.dropped.Add(uint64(len(rows) - room)) //nolint:gosec // room <= len(rows), checked above
		rows = rows[:room]
	}
	w.pending = append(w.pending, rows...)
	w.queued += len(rows)
}

// Run flushes until ctx is cancelled, then makes one last attempt.
func (w *Writer) Run(ctx context.Context) {
	every := w.Every
	if every <= 0 {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			w.flush(final)
			cancel()
			return
		case <-t.C:
			w.flush(ctx)
		}
	}
}

func (w *Writer) flush(ctx context.Context) {
	w.mu.Lock()
	for len(w.pending) > 0 {
		n := min(len(w.pending), batchRows)
		w.seq++
		w.retry = append(w.retry, batch{
			token: fmt.Sprintf("netinv-%d-%d", time.Now().UnixNano(), w.seq),
			rows:  w.pending[:n:n],
		})
		w.pending = w.pending[n:]
	}
	w.pending = nil
	todo := w.retry
	w.mu.Unlock()

	sent := 0
	for _, b := range todo {
		body, err := encode(b.rows)
		if err == nil {
			err = w.C.Insert(ctx, Database+".flows", body, map[string]string{
				"insert_deduplication_token": b.token,
				// Inserting into the raw table also feeds the rollup view;
				// the token must reach it too, or a retried batch would be
				// deduplicated in one table and counted twice in the other.
				"deduplicate_blocks_in_dependent_materialized_views": "1",
			})
		}
		if err != nil {
			w.failed.Add(1)
			w.Log.Warn("flow raw insert failed; will retry", "rows", len(b.rows), "err", err)
			break // keep order: later batches wait behind this one
		}
		w.written.Add(uint64(len(b.rows)))
		sent++
	}

	w.mu.Lock()
	for _, b := range w.retry[:sent] {
		w.queued -= len(b.rows)
	}
	w.retry = w.retry[sent:]
	w.mu.Unlock()
}

// Stats reports totals since start and the rows currently held.
func (w *Writer) Stats() (written, dropped, failed uint64, queued int) {
	w.mu.Lock()
	q := w.queued
	w.mu.Unlock()
	return w.written.Load(), w.dropped.Load(), w.failed.Load(), q
}

// ReportStatus writes the collector's counters to collector_status once a
// minute, for the API's status endpoint, and logs any drops.
func (w *Writer) ReportStatus(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	var lastDropped uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		written, dropped, failed, queued := w.Stats()
		if dropped > lastDropped {
			w.Log.Warn("flow raw queue full — flows dropped from raw storage",
				"dropped", dropped-lastDropped, "queued", queued,
				"hint", "the aggregates are unaffected; check ClickHouse")
		}
		lastDropped = dropped
		asn, country := w.Geo().Loaded()
		st := map[string]any{
			"at": time.Now().UTC().Format("2006-01-02 15:04:05"), "written": written,
			"dropped": dropped, "failed": failed, "queued": queued,
			"geo_asn": asn, "geo_country": country,
		}
		body, _ := json.Marshal(st)
		if err := w.C.Insert(ctx, Database+".collector_status", body, nil); err != nil {
			w.Log.Debug("flow status write failed", "err", err)
		}
	}
}

// row is one flows-table row in JSONEachRow form.
type row struct {
	Received   string `json:"received"`
	TimeStart  string `json:"time_start"`
	TimeEnd    string `json:"time_end"`
	Exporter   string `json:"exporter"`
	Type       string `json:"type"`
	Sampling   uint32 `json:"sampling"`
	InIf       uint32 `json:"in_if"`
	OutIf      uint32 `json:"out_if"`
	SrcAddr    string `json:"src_addr"`
	DstAddr    string `json:"dst_addr"`
	SrcPort    uint16 `json:"src_port"`
	DstPort    uint16 `json:"dst_port"`
	Proto      uint8  `json:"proto"`
	TOS        uint8  `json:"tos"`
	TCPFlags   uint8  `json:"tcp_flags"`
	SrcAS      uint32 `json:"src_as"`
	DstAS      uint32 `json:"dst_as"`
	SrcASName  string `json:"src_as_name"`
	DstASName  string `json:"dst_as_name"`
	SrcCountry string `json:"src_country"`
	DstCountry string `json:"dst_country"`
	SrcMask    uint8  `json:"src_mask"`
	DstMask    uint8  `json:"dst_mask"`
	SrcMAC     uint64 `json:"src_mac"`
	DstMAC     uint64 `json:"dst_mac"`
	SrcVLAN    uint16 `json:"src_vlan"`
	DstVLAN    uint16 `json:"dst_vlan"`
	NextHop    string `json:"next_hop"`
	BGPNextHop string `json:"bgp_next_hop"`
	Direction  uint8  `json:"direction"`
	ICMPType   uint16 `json:"icmp_type"`
	AppID      string `json:"app_id"`
	AppName    string `json:"app_name"`
	NATSrcAddr string `json:"nat_src_addr"`
	NATDstAddr string `json:"nat_dst_addr"`
	NATSrcPort uint16 `json:"nat_src_port"`
	NATDstPort uint16 `json:"nat_dst_port"`
	FWEvent    uint8  `json:"fw_event"`
	User       string `json:"user"`
	Bytes      uint64 `json:"bytes"`
	Packets    uint64 `json:"packets"`
}

const chTime = "2006-01-02 15:04:05.000"

func chTimeOrZero(t time.Time) string {
	if t.IsZero() {
		return "1970-01-01 00:00:00.000"
	}
	return t.UTC().Format(chTime)
}

// chAddr renders an address for an IPv6 column. IPv4 is written as such and
// stored IPv4-mapped; no address is the unspecified one.
func chAddr(a netip.Addr) string {
	if !a.IsValid() {
		return "::"
	}
	return a.Unmap().String()
}

func toRow(now time.Time, exporter netip.Addr, r *flow.Record, g *geo.DB) row {
	rw := row{
		Received: now.Format(chTime), TimeStart: chTimeOrZero(r.Start), TimeEnd: chTimeOrZero(r.End),
		Exporter: chAddr(exporter), Type: r.Type, Sampling: max(r.SamplingRate, 1),
		InIf: r.InputIf, OutIf: r.OutputIf,
		SrcAddr: chAddr(r.SrcAddr), DstAddr: chAddr(r.DstAddr),
		SrcPort: r.SrcPort, DstPort: r.DstPort, Proto: r.Protocol, TOS: r.TOS,
		TCPFlags: r.TCPFlags, SrcAS: r.SrcAS, DstAS: r.DstAS,
		SrcMask: r.SrcMask, DstMask: r.DstMask, SrcMAC: r.SrcMAC, DstMAC: r.DstMAC,
		SrcVLAN: r.SrcVLAN, DstVLAN: r.DstVLAN,
		NextHop: chAddr(r.NextHop), BGPNextHop: chAddr(r.BGPNextHop),
		Direction: r.Direction, ICMPType: r.ICMPType, AppID: r.AppID, AppName: r.AppName,
		NATSrcAddr: chAddr(r.NATSrcAddr), NATDstAddr: chAddr(r.NATDstAddr),
		NATSrcPort: r.NATSrcPort, NATDstPort: r.NATDstPort, FWEvent: r.FWEvent,
		User: r.User, Bytes: r.Bytes, Packets: r.Packets,
	}
	if g != nil {
		src, dst := g.Lookup(r.SrcAddr), g.Lookup(r.DstAddr)
		rw.SrcCountry, rw.DstCountry = src.Country, dst.Country
		// The exporter's BGP view wins where it has one; the offline table
		// fills the gap that most edge devices leave.
		if rw.SrcAS == 0 {
			rw.SrcAS = src.ASN
		}
		if rw.DstAS == 0 {
			rw.DstAS = dst.ASN
		}
		rw.SrcASName = g.ASName(rw.SrcAS)
		rw.DstASName = g.ASName(rw.DstAS)
	}
	return rw
}

func encode(rows []row) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(rows) * 600)
	enc := json.NewEncoder(&buf)
	for i := range rows {
		if err := enc.Encode(&rows[i]); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// parseUint reads a JSON number from a Select row.
func parseUint(v any) uint64 {
	switch n := v.(type) {
	case json.Number:
		u, _ := strconv.ParseUint(n.String(), 10, 64)
		return u
	case string:
		u, _ := strconv.ParseUint(n, 10, 64)
		return u
	}
	return 0
}
