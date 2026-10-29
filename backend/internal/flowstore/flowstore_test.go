package flowstore

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/flow"
)

func TestPrefixRangeMapsIPv4AndCoversThePrefix(t *testing.T) {
	lo, hi := prefixRange(netip.MustParsePrefix("10.1.0.0/16"))
	if lo != "::ffff:10.1.0.0" || hi != "::ffff:10.1.255.255" {
		t.Fatalf("v4: got %s..%s", lo, hi)
	}
	lo, hi = prefixRange(netip.MustParsePrefix("2001:db8::/32"))
	if lo != "2001:db8::" || hi != "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff" {
		t.Fatalf("v6: got %s..%s", lo, hi)
	}
}

func TestCompileRefusesWhatIsNotInTheVocabulary(t *testing.T) {
	now := time.Now()
	base := Query{From: now.Add(-time.Hour), To: now}
	for name, q := range map[string]Query{
		"unknown group":   {GroupBy: []string{"src_addr; DROP TABLE x"}},
		"filter-only":     {GroupBy: []string{"addr"}},
		"unknown filter":  {Filters: []Filter{{Field: "nope", Values: []string{"1"}}}},
		"bad address":     {Filters: []Filter{{Field: "src_addr", Values: []string{"1.2.3"}}}},
		"bad number":      {Filters: []Filter{{Field: "dst_port", Values: []string{"https"}}}},
		"empty values":    {Filters: []Filter{{Field: "dst_port"}}},
		"bad metric":      {Metric: "bits"},
		"too many groups": {GroupBy: []string{"src_addr", "dst_addr", "proto", "src_port", "dst_port"}},
	} {
		q.From, q.To = base.From, base.To
		if _, err := compile(&q); err == nil {
			t.Errorf("%s: compiled, want refusal", name)
		}
	}
}

func TestValuesTravelAsParametersNotSQL(t *testing.T) {
	now := time.Now()
	q := Query{From: now.Add(-time.Hour), To: now,
		Filters: []Filter{{Field: "user", Values: []string{"x' OR 1=1 --"}}}}
	b, err := compile(&q)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.where, "OR 1=1") {
		t.Fatalf("value reached the SQL text: %s", b.where)
	}
	if b.params["f0_0"] != "x' OR 1=1 --" {
		t.Fatalf("value not passed as a parameter: %v", b.params)
	}
}

func TestTableChoice(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-30 * 24 * time.Hour)
	day := Query{From: now.Add(-24 * time.Hour), To: now, RawCutoff: cutoff}
	if tb, _ := chooseTable(&day); tb != TableRaw {
		t.Errorf("a day: %s, want raw", tb)
	}
	month := Query{From: now.Add(-20 * 24 * time.Hour), To: now, RawCutoff: cutoff}
	if tb, _ := chooseTable(&month); tb != TableRollup {
		t.Errorf("20 days of rollup fields: %s, want rollup", tb)
	}
	month.GroupBy = []string{"src_mac"}
	if tb, _ := chooseTable(&month); tb != TableRaw {
		t.Errorf("20 days by MAC: %s, want raw", tb)
	}
	year := Query{From: now.Add(-200 * 24 * time.Hour), To: now, RawCutoff: cutoff, GroupBy: []string{"user"}}
	if _, err := chooseTable(&year); err == nil {
		t.Error("user past the raw retention: want a refusal naming the field")
	}
}

// The rest runs against a real ClickHouse at NETINV_TEST_CH_URL. The schema
// name is fixed, so each test drops it first: never point this at a
// deployment you care about.
func testClient(t *testing.T) *Client {
	t.Helper()
	u := os.Getenv("NETINV_TEST_CH_URL")
	if u == "" {
		t.Skip("NETINV_TEST_CH_URL not set")
	}
	c := NewClient(u, os.Getenv("NETINV_TEST_CH_USER"), os.Getenv("NETINV_TEST_CH_PASSWORD"))
	if err := c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+Database+" SYNC", nil); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSchemaIsIdempotentAndFollowsRetention(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	if err := EnsureSchema(ctx, c, DefaultRetention); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(ctx, c, DefaultRetention); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if err := EnsureSchema(ctx, c, Retention{RawDays: 7, RollupDays: 90}); err != nil {
		t.Fatal(err)
	}
	rows, err := c.Select(ctx, `SELECT name, create_table_query AS q FROM system.tables
		WHERE database = {db:String} AND name IN ('flows', 'flows_1h')`, map[string]string{"db": Database})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		q := r["q"].(string)
		want := map[string]string{"flows": "toIntervalDay(7)", "flows_1h": "toIntervalDay(90)"}[r["name"].(string)]
		if !strings.Contains(q, want) {
			t.Errorf("%s TTL not updated: %s", r["name"], q)
		}
	}
}

func TestWriteThenExplore(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	if err := EnsureSchema(ctx, c, DefaultRetention); err != nil {
		t.Fatal(err)
	}
	w := &Writer{C: c, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	exp := netip.MustParseAddr("192.0.2.1")
	mk := func(src, dst string, port uint16, bytes uint64) flow.Record {
		return flow.Record{
			SrcAddr: netip.MustParseAddr(src), DstAddr: netip.MustParseAddr(dst),
			SrcPort: 50000, DstPort: port, Protocol: 6, Bytes: bytes, Packets: bytes / 100,
			InputIf: 1, OutputIf: 2, Type: "netflow9", SamplingRate: 1,
			SrcMAC: 0x001122334455, User: "alice",
		}
	}
	w.Add(&flow.Packet{ExporterIP: exp, Records: []flow.Record{
		mk("10.0.0.1", "203.0.113.5", 443, 9000),
		mk("10.0.0.1", "203.0.113.5", 443, 1000),
		mk("10.0.0.2", "203.0.113.9", 53, 500),
		mk("2001:db8::1", "2001:db8::2", 22, 300),
	}})
	w.flush(ctx)
	// Flushing again sends nothing: a batch is retired once accepted.
	w.flush(ctx)
	if written, dropped, failed, queued := w.Stats(); written != 4 || dropped != 0 || failed != 0 || queued != 0 {
		t.Fatalf("stats written=%d dropped=%d failed=%d queued=%d", written, dropped, failed, queued)
	}

	now := time.Now()
	res, err := Explore(ctx, c, &Query{
		From: now.Add(-time.Hour), To: now.Add(time.Minute),
		GroupBy: []string{"src_addr", "dst_port"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total.Bytes != 10800 || res.Total.Flows != 4 {
		t.Fatalf("total %+v", res.Total)
	}
	top := res.Groups[0]
	if top.Keys[0] != "10.0.0.1" || top.Keys[1] != "443" || top.Bytes != 10000 || top.Flows != 2 {
		t.Fatalf("top group %+v", top)
	}
	if len(res.Series.Lines) != len(res.Groups) || len(res.Series.Times) == 0 {
		t.Fatalf("series %+v", res.Series)
	}

	// Filters: a prefix, a negation, an either-end field, and an exporter set.
	res, err = Explore(ctx, c, &Query{
		From: now.Add(-time.Hour), To: now.Add(time.Minute),
		Filters: []Filter{
			{Field: "addr", Values: []string{"10.0.0.0/24"}},
			{Field: "dst_port", Negate: true, Values: []string{"53"}},
		},
		Exporters: []netip.Addr{exp},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total.Bytes != 10000 {
		t.Fatalf("filtered total %d, want 10000", res.Total.Bytes)
	}
	res, err = Explore(ctx, c, &Query{From: now.Add(-time.Hour), To: now.Add(time.Minute), Exporters: []netip.Addr{}})
	if err != nil || res.Total.Bytes != 0 {
		t.Fatalf("an empty exporter set must match nothing: %v %+v", err, res)
	}

	// An interface is keyed by its exporter, and that key filters back.
	res, err = Explore(ctx, c, &Query{From: now.Add(-time.Hour), To: now.Add(time.Minute), GroupBy: []string{"in_if"},
		Filters: []Filter{{Field: "in_if", Values: []string{"192.0.2.1|1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Keys[0] != "192.0.2.1|1" || res.Total.Bytes != 10800 {
		t.Fatalf("interface grouping %+v total %+v", res.Groups, res.Total)
	}

	recs, err := Records(ctx, c, &RecordsQuery{Query: Query{From: now.Add(-time.Hour), To: now.Add(time.Minute),
		Filters: []Filter{{Field: "user", Values: []string{"alice"}}}}, OrderBy: "bytes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 || recs[0]["src_mac"] != "00:11:22:33:44:55" || recs[0]["exporter"] != "192.0.2.1" {
		t.Fatalf("records %v", recs)
	}

	// The rollup view saw the same rows, with the ephemeral source port folded.
	r, err := c.Select(ctx, "SELECT sum(bytes) AS b, sum(flows) AS f, max(src_port) AS p FROM "+Database+".flows_1h", nil)
	if err != nil {
		t.Fatal(err)
	}
	if parseUint(r[0]["b"]) != 10800 || parseUint(r[0]["f"]) != 4 || parseUint(r[0]["p"]) != 0 {
		t.Fatalf("rollup %v", r[0])
	}
}

func TestRetriedBatchIsNotCountedTwice(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	if err := EnsureSchema(ctx, c, DefaultRetention); err != nil {
		t.Fatal(err)
	}
	body, _ := encode([]row{toRow(time.Now().UTC(), netip.MustParseAddr("192.0.2.1"), &flow.Record{
		SrcAddr: netip.MustParseAddr("10.0.0.1"), DstAddr: netip.MustParseAddr("10.0.0.2"), Bytes: 100,
	}, nil)})
	settings := map[string]string{"insert_deduplication_token": "same",
		"deduplicate_blocks_in_dependent_materialized_views": "1"}
	for i := 0; i < 2; i++ {
		if err := c.Insert(ctx, Database+".flows", body, settings); err != nil {
			t.Fatal(err)
		}
	}
	for _, tb := range []string{"flows", "flows_1h"} {
		r, err := c.Select(ctx, "SELECT sum(bytes) AS b FROM "+Database+"."+tb, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := parseUint(r[0]["b"]); got != 100 {
			t.Errorf("%s: %d bytes after a retried insert, want 100", tb, got)
		}
	}
}

func TestQueueBoundDropsAndCounts(t *testing.T) {
	w := &Writer{MaxQueued: 3, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	recs := make([]flow.Record, 5)
	w.Add(&flow.Packet{ExporterIP: netip.MustParseAddr("192.0.2.1"), Records: recs})
	if _, dropped, _, queued := w.Stats(); dropped != 2 || queued != 3 {
		t.Fatalf("dropped=%d queued=%d, want 2 and 3", dropped, queued)
	}
}

func TestGroupByASShowsTheName(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	if err := EnsureSchema(ctx, c, DefaultRetention); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rw := toRow(now, netip.MustParseAddr("192.0.2.1"), &flow.Record{
		SrcAddr: netip.MustParseAddr("10.0.0.1"), DstAddr: netip.MustParseAddr("1.1.1.1"),
		DstAS: 13335, Bytes: 700, Type: "netflow9",
	}, nil)
	rw.DstASName = "CLOUDFLARENET"
	body, _ := encode([]row{rw})
	if err := c.Insert(ctx, Database+".flows", body, nil); err != nil {
		t.Fatal(err)
	}
	res, err := Explore(ctx, c, &Query{From: now.Add(-time.Hour), To: now.Add(time.Minute), GroupBy: []string{"dst_as"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Keys[0] != "13335 CLOUDFLARENET" || res.Series.Lines[0][0] == 0 {
		t.Fatalf("groups %+v series %+v", res.Groups, res.Series)
	}
}
