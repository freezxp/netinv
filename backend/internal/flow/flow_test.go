package flow

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// v5Packet builds a real NetFlow v5 export packet, so the decoder is tested
// against the wire format rather than against a struct someone typed twice.
func v5Packet(t *testing.T, samplingMode, samplingInterval uint16, recs ...Record) []byte {
	t.Helper()
	b := make([]byte, netflowV5HeaderLen+len(recs)*netflowV5RecordLen)
	binary.BigEndian.PutUint16(b[0:2], 5)
	binary.BigEndian.PutUint16(b[2:4], uint16(len(recs)))
	binary.BigEndian.PutUint32(b[4:8], 1000)
	binary.BigEndian.PutUint32(b[8:12], 1786000000)
	binary.BigEndian.PutUint16(b[22:24], samplingMode<<14|samplingInterval)
	for i, r := range recs {
		o := b[netflowV5HeaderLen+i*netflowV5RecordLen:]
		copy(o[0:4], r.SrcAddr.AsSlice())
		copy(o[4:8], r.DstAddr.AsSlice())
		binary.BigEndian.PutUint16(o[12:14], uint16(r.InputIf))
		binary.BigEndian.PutUint16(o[14:16], uint16(r.OutputIf))
		binary.BigEndian.PutUint32(o[16:20], uint32(r.Packets))
		binary.BigEndian.PutUint32(o[20:24], uint32(r.Bytes))
		binary.BigEndian.PutUint16(o[32:34], r.SrcPort)
		binary.BigEndian.PutUint16(o[34:36], r.DstPort)
		o[38] = r.Protocol
	}
	return b
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestDecodeNetFlowV5(t *testing.T) {
	pkt := v5Packet(t, 0, 0,
		Record{SrcAddr: addr("10.0.0.1"), DstAddr: addr("10.0.0.2"),
			SrcPort: 51000, DstPort: 443, Protocol: 6,
			Bytes: 1500, Packets: 3, InputIf: 2, OutputIf: 3},
	)
	p, err := DecodeNetFlowV5(pkt, addr("192.0.2.1"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(p.Records))
	}
	r := p.Records[0]
	if r.SrcAddr.String() != "10.0.0.1" || r.DstAddr.String() != "10.0.0.2" {
		t.Errorf("addresses = %v → %v", r.SrcAddr, r.DstAddr)
	}
	if r.Bytes != 1500 || r.Packets != 3 {
		t.Errorf("bytes/packets = %d/%d, want 1500/3", r.Bytes, r.Packets)
	}
	if r.DstPort != 443 || r.Protocol != 6 {
		t.Errorf("dst/proto = %d/%d", r.DstPort, r.Protocol)
	}
	if r.Sampled {
		t.Error("marked sampled with sampling disabled")
	}
	// The exporter is the datagram source: v5 carries no exporter identity,
	// and a field inside the packet would be trivially spoofable.
	if p.ExporterIP.String() != "192.0.2.1" {
		t.Errorf("exporter = %v, want the datagram source", p.ExporterIP)
	}
}

// A sampled exporter reports one flow in N. Recording the raw count would
// understate the link by exactly the sampling rate.
func TestDecodeScalesSampledCounts(t *testing.T) {
	pkt := v5Packet(t, 1, 100,
		Record{SrcAddr: addr("10.0.0.1"), DstAddr: addr("10.0.0.2"),
			Bytes: 1000, Packets: 2, InputIf: 1, Protocol: 6},
	)
	p, err := DecodeNetFlowV5(pkt, addr("192.0.2.1"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r := p.Records[0]
	if r.Bytes != 100_000 || r.Packets != 200 {
		t.Errorf("bytes/packets = %d/%d, want them scaled by 100", r.Bytes, r.Packets)
	}
	if !r.Sampled {
		t.Error("scaled counts must be marked sampled so the UI can say so")
	}
}

// An interval of 0 or 1 means one-for-one. Treating it as a multiplier would
// zero every byte count, or leave it unchanged while claiming it was sampled.
func TestDecodeTreatsIntervalOneAsUnsampled(t *testing.T) {
	for _, iv := range []uint16{0, 1} {
		pkt := v5Packet(t, 1, iv,
			Record{SrcAddr: addr("10.0.0.1"), DstAddr: addr("10.0.0.2"),
				Bytes: 1000, Packets: 2, InputIf: 1},
		)
		p, err := DecodeNetFlowV5(pkt, addr("192.0.2.1"))
		if err != nil {
			t.Fatalf("interval %d: %v", iv, err)
		}
		if got := p.Records[0].Bytes; got != 1000 {
			t.Errorf("interval %d: bytes = %d, want 1000 unchanged", iv, got)
		}
		if p.Records[0].Sampled {
			t.Errorf("interval %d: marked sampled", iv)
		}
	}
}

// A count field is attacker-controlled. Believing it would let a 24-byte
// datagram claim 65535 records and drive an enormous allocation.
func TestDecodeRejectsImpossibleRecordCounts(t *testing.T) {
	b := make([]byte, netflowV5HeaderLen)
	binary.BigEndian.PutUint16(b[0:2], 5)
	binary.BigEndian.PutUint16(b[2:4], 65535)
	if _, err := DecodeNetFlowV5(b, addr("192.0.2.1")); err == nil {
		t.Fatal("a packet claiming 65535 records was accepted")
	}

	// Truncated: header says two records, only one is present.
	good := v5Packet(t, 0, 0,
		Record{SrcAddr: addr("10.0.0.1"), DstAddr: addr("10.0.0.2"), InputIf: 1},
		Record{SrcAddr: addr("10.0.0.3"), DstAddr: addr("10.0.0.4"), InputIf: 1})
	if _, err := DecodeNetFlowV5(good[:len(good)-10], addr("192.0.2.1")); err == nil {
		t.Fatal("a truncated packet was accepted")
	}
	if _, err := DecodeNetFlowV5([]byte{0, 9}, addr("192.0.2.1")); err == nil {
		t.Fatal("a v9 packet was accepted by the v5 decoder")
	}
}

func TestAggregateTopNAndDimensions(t *testing.T) {
	a := NewAggregator()
	a.TopN = 2
	mk := func(src, dst string, port uint16, bytes uint64) Record {
		return Record{SrcAddr: addr(src), DstAddr: addr(dst), DstPort: port,
			SrcPort: 50000, Protocol: 6, Bytes: bytes, Packets: 1, InputIf: 2}
	}
	a.Add(&Packet{ExporterIP: addr("192.0.2.1"), Records: []Record{
		mk("10.0.0.1", "10.0.0.9", 443, 1000),
		mk("10.0.0.2", "10.0.0.9", 443, 500),
		mk("10.0.0.3", "10.0.0.9", 22, 10),
	}})

	byDim := map[Dimension][]Bucket{}
	for _, b := range a.Drain() {
		byDim[b.Key.Dimension] = append(byDim[b.Key.Dimension], b)
	}
	for _, d := range []Dimension{DimTalker, DimConversation, DimApplication} {
		if len(byDim[d]) == 0 {
			t.Errorf("no buckets for dimension %q", d)
		}
		if len(byDim[d]) > 2 {
			t.Errorf("dimension %q returned %d buckets, TopN is 2 — the cut is what "+
				"keeps cardinality bounded", d, len(byDim[d]))
		}
	}
	// 10.0.0.9 is on every flow, so it must be the top talker at 1510 bytes.
	top := byDim[DimTalker][0]
	if top.Key.Value != "10.0.0.9" || top.Bytes != 1510 {
		t.Errorf("top talker = %s at %d bytes, want 10.0.0.9 at 1510",
			top.Key.Value, top.Bytes)
	}
	// Well-known ports are named; the ephemeral source port must not become
	// the key or every connection would be its own bucket.
	var names []string
	for _, b := range byDim[DimApplication] {
		names = append(names, b.Key.Value)
	}
	if names[0] != "https" {
		t.Errorf("top application = %q, want https", names[0])
	}
}

// A→B and B→A are one exchange. Keying them separately would split every
// conversation in half and halve its apparent size.
func TestConversationsAreDirectionless(t *testing.T) {
	a := NewAggregator()
	a.Add(&Packet{ExporterIP: addr("192.0.2.1"), Records: []Record{
		{SrcAddr: addr("10.0.0.1"), DstAddr: addr("10.0.0.2"), Bytes: 100, InputIf: 1},
		{SrcAddr: addr("10.0.0.2"), DstAddr: addr("10.0.0.1"), Bytes: 400, InputIf: 1},
	}})
	var convs []Bucket
	for _, b := range a.Drain() {
		if b.Key.Dimension == DimConversation {
			convs = append(convs, b)
		}
	}
	if len(convs) != 1 {
		t.Fatalf("got %d conversation buckets, want the two directions merged", len(convs))
	}
	if convs[0].Bytes != 500 {
		t.Errorf("conversation bytes = %d, want both directions summed to 500", convs[0].Bytes)
	}
}

// A flow with neither ingress nor egress interface cannot be attributed. It
// must be dropped rather than land on ifIndex 0, which renders as a real
// interface on the dashboard.
func TestUnattributableFlowsAreDropped(t *testing.T) {
	a := NewAggregator()
	a.Add(&Packet{ExporterIP: addr("192.0.2.1"), Records: []Record{
		{SrcAddr: addr("10.0.0.1"), DstAddr: addr("10.0.0.2"), Bytes: 100},
	}})
	if got := a.Drain(); len(got) != 0 {
		t.Errorf("got %d buckets from an unattributable flow, want none", len(got))
	}
}

// The key cap stops a scan or a spoofed source growing the map without bound
// before the next drain. Traffic past the cap is folded into "other" so the
// totals stay honest even though the detail is gone.
func TestKeyCapFoldsIntoOtherRatherThanDropping(t *testing.T) {
	a := NewAggregator()
	a.MaxKeys = 8
	a.TopN = 100
	recs := make([]Record, 0, 50)
	for i := 0; i < 50; i++ {
		recs = append(recs, Record{
			SrcAddr: netip.AddrFrom4([4]byte{10, 1, byte(i / 256), byte(i % 256)}),
			DstAddr: addr("10.0.0.9"), Bytes: 10, Packets: 1, InputIf: 1,
		})
	}
	a.Add(&Packet{ExporterIP: addr("192.0.2.1"), Records: recs})
	if a.Dropped() == 0 {
		t.Error("nothing was folded into other despite exceeding MaxKeys")
	}
	var total uint64
	var sawOther bool
	for _, b := range a.Drain() {
		if b.Key.Dimension != DimTalker {
			continue
		}
		total += b.Bytes
		if b.Key.Value == OtherValue {
			sawOther = true
		}
	}
	if !sawOther {
		t.Error("no 'other' bucket; traffic past the cap vanished silently")
	}
	if total == 0 {
		t.Error("no bytes survived the cap at all")
	}
}

type fakeWriter struct{ got []Bucket }

func (f *fakeWriter) WriteFlow(_ context.Context, _ time.Time, b []Bucket) error {
	f.got = append(f.got, b...)
	return nil
}

// The collector is the first component accepting unsolicited network input.
// Without the allow-list anyone who can reach the port can write series.
func TestAllowListRefusesUnlistedSources(t *testing.T) {
	c := &Collector{Allow: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}
	if !c.allowed(addr("192.0.2.55")) {
		t.Error("a listed source was refused")
	}
	if c.allowed(addr("198.51.100.1")) {
		t.Error("an unlisted source was accepted")
	}
	// Empty means accept anything — the documented default.
	open := &Collector{}
	if !open.allowed(addr("198.51.100.1")) {
		t.Error("empty allow-list should accept any source")
	}
}

func TestParseAllowAcceptsBareAddressesAndCIDRs(t *testing.T) {
	got, err := ParseAllow("192.0.2.1, 10.0.0.0/8 ,2001:db8::1")
	if err != nil {
		t.Fatalf("ParseAllow: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d prefixes, want 3", len(got))
	}
	// A bare address means exactly that host, not its whole network.
	if got[0].Bits() != 32 {
		t.Errorf("bare IPv4 became /%d, want /32", got[0].Bits())
	}
	if got[2].Bits() != 128 {
		t.Errorf("bare IPv6 became /%d, want /128", got[2].Bits())
	}
	if _, err := ParseAllow("not-an-address"); err == nil {
		t.Error("nonsense in the allow-list was accepted")
	}
}

// --- collector round trip -------------------------------------------------

type captureWriter struct {
	mu      sync.Mutex
	buckets []Bucket
	done    chan struct{}
	once    sync.Once
}

func (c *captureWriter) WriteFlow(_ context.Context, _ time.Time, b []Bucket) error {
	c.mu.Lock()
	c.buckets = append(c.buckets, b...)
	c.mu.Unlock()
	c.once.Do(func() { close(c.done) })
	return nil
}

// The receive → decode → aggregate → write path, over a real UDP socket.
//
// This is the path that was validated by hand against a live collector, and
// every fault found while doing so was invisible: a generator emitting 46-byte
// records (the spec says 48) was rejected as truncated and logged at debug, so
// the collector looked idle rather than broken. A test that only exercised
// DecodeNetFlowV5 directly would not have caught any of it.
func TestCollectorReceivesAggregatesAndWrites(t *testing.T) {
	w := &captureWriter{done: make(chan struct{})}
	c := &Collector{
		Addr:  "127.0.0.1:0",
		Write: w,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Every: 50 * time.Millisecond,
	}

	// Bind first so the test knows the port, rather than racing Run's listen.
	pc, err := net.ListenPacket("udp", c.Addr)
	if err != nil {
		t.Fatal(err)
	}
	addrStr := pc.LocalAddr().String()
	pc.Close()
	c.Addr = addrStr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	conn, err := net.Dial("udp", addrStr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pkt := v5Packet(t, 0, 0,
		Record{SrcAddr: addr("10.0.0.1"), DstAddr: addr("10.0.0.2"), InputIf: 3,
			SrcPort: 51000, DstPort: 443, Protocol: 6, Bytes: 1000, Packets: 10},
		Record{SrcAddr: addr("10.0.0.3"), DstAddr: addr("10.0.0.2"), InputIf: 3,
			SrcPort: 52000, DstPort: 22, Protocol: 6, Bytes: 500, Packets: 5},
	)
	// Resend until a drain fires. The port is probed and released before Run
	// rebinds it, so the first writes can land on a closed socket and come back
	// as ECONNREFUSED via ICMP — transient, and not what this test is about.
	deadline := time.After(5 * time.Second)
	for {
		_, _ = conn.Write(pkt)
		select {
		case <-w.done:
			goto drained
		case <-deadline:
			t.Fatal("no buckets written within 5s")
		case <-time.After(25 * time.Millisecond):
		}
	}
drained:
	cancel()

	w.mu.Lock()
	defer w.mu.Unlock()
	var sawTalker, sawConv, sawApp bool
	for _, b := range w.buckets {
		if b.Key.IfIndex != 3 {
			t.Errorf("bucket on ifIndex %d, want 3", b.Key.IfIndex)
		}
		switch b.Key.Dimension {
		case DimTalker:
			sawTalker = true
		case DimConversation:
			sawConv = true
		case DimApplication:
			sawApp = true
			if b.Key.Value == "https" && b.Bytes == 0 {
				t.Error("https bucket carried no bytes")
			}
		}
	}
	if !sawTalker || !sawConv || !sawApp {
		t.Errorf("missing dimensions: talker=%v conversation=%v application=%v",
			sawTalker, sawConv, sawApp)
	}
}

// A datagram from a source outside the allow-list must never reach the
// aggregator — the allow-list is the only thing standing between an open UDP
// port and attacker-controlled series (doc 34 §6).
func TestCollectorRefusesDisallowedSource(t *testing.T) {
	c := &Collector{
		Agg:   NewAggregator(),
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Allow: mustAllow(t, "192.0.2.0/24"),
	}
	pkt := v5Packet(t, 0, 0, Record{SrcAddr: addr("10.0.0.1"),
		DstAddr: addr("10.0.0.2"), InputIf: 1, Bytes: 100, Packets: 1})
	c.handle(pkt, &net.UDPAddr{IP: net.ParseIP("198.51.100.7"), Port: 5000})
	if _, _, refused, _ := c.Stats(); refused != 1 {
		t.Errorf("refused=%d, want 1", refused)
	}
	if got := c.Agg.Drain(); len(got) != 0 {
		t.Errorf("a refused packet produced %d buckets", len(got))
	}

	c.handle(pkt, &net.UDPAddr{IP: net.ParseIP("192.0.2.9"), Port: 5000})
	if pkts, recs, _, _ := c.Stats(); pkts != 1 || recs != 1 {
		t.Errorf("allowed packet: packets=%d records=%d, want 1/1", pkts, recs)
	}
}

// An export version the collector cannot decode must be counted, not ignored:
// "an exporter is sending me v9" and "nothing is arriving" are different
// operational problems and must not look identical (doc 34 §5).
func TestUndecodableVersionsAreCounted(t *testing.T) {
	c := &Collector{Agg: NewAggregator(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, b := range [][]byte{
		{0, 9, 0, 1},  // NetFlow v9 — not implemented
		{0, 10, 0, 1}, // IPFIX — not implemented
		{0},           // too short even for a version
		append([]byte{0, 5, 0, 30}, make([]byte, 10)...), // v5 claiming 30 records it does not carry
	} {
		c.handle(b, &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 5000})
	}
	if _, _, _, malformed := c.Stats(); malformed != 4 {
		t.Errorf("malformed=%d, want 4", malformed)
	}
}

// --- VM writer ------------------------------------------------------------

func TestVMWriterEmitsBothMetricsWithLabels(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/import" {
			t.Errorf("posted to %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	w := NewVMWriter(srv.URL)
	err := w.WriteFlow(context.Background(), time.Unix(1700000000, 0), []Bucket{
		{Key: Key{"192.0.2.1", 3, DimTalker, "10.0.0.1"}, Bytes: 1000, Packets: 10},
		{Key: Key{"192.0.2.1", 3, DimApplication, "https"}, Bytes: 900, Packets: 9, Sampled: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	var lines []importLine
	for _, l := range strings.Split(strings.TrimSpace(body), "\n") {
		var il importLine
		if err := json.Unmarshal([]byte(l), &il); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		lines = append(lines, il)
	}
	// Two metrics per bucket, and both must be present: the recurring bug in
	// this codebase is a second metric vanishing, not a first one missing.
	if len(lines) != 4 {
		t.Fatalf("got %d import lines, want 4", len(lines))
	}
	names := map[string]int{}
	for _, l := range lines {
		names[l.Metric["__name__"]]++
		if l.Metric["exporter"] != "192.0.2.1" || l.Metric["if_index"] != "3" {
			t.Errorf("bad labels: %v", l.Metric)
		}
		if l.Timestamps[0] != 1700000000000 {
			t.Errorf("timestamp %d, want ms", l.Timestamps[0])
		}
	}
	if names[MetricBytes] != 2 || names[MetricPackets] != 2 {
		t.Errorf("metric counts: %v", names)
	}
	// Sampled travels as a label so the UI can say a number is an estimate.
	if !strings.Contains(body, `"sampled":"true"`) {
		t.Error("sampled bucket lost its sampled label")
	}
	if strings.Count(body, `"sampled":"true"`) != 2 {
		t.Error("sampled label should be on both metrics of the sampled bucket")
	}
}

func TestVMWriterReportsStoreErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	err := NewVMWriter(srv.URL).WriteFlow(context.Background(), time.Now(),
		[]Bucket{{Key: Key{"192.0.2.1", 1, DimTalker, "10.0.0.1"}, Bytes: 1}})
	if err == nil {
		t.Fatal("a 500 from the metrics store must be reported, not swallowed")
	}
}

func TestVersionAndProtoNames(t *testing.T) {
	if v, ok := Version([]byte{0, 5, 0, 1}); !ok || v != 5 {
		t.Errorf("Version = %d, %v", v, ok)
	}
	if _, ok := Version([]byte{0}); ok {
		t.Error("a one-byte datagram has no readable version")
	}
	if got := (ErrShortPacket{Want: 48, Got: 46}).Error(); !strings.Contains(got, "46") {
		t.Errorf("ErrShortPacket message unhelpful: %q", got)
	}
	for proto, want := range map[uint8]string{1: "icmp", 6: "tcp", 17: "udp",
		47: "gre", 50: "esp", 58: "icmp6", 89: "ip/89"} {
		if got := protoName(proto); got != want {
			t.Errorf("protoName(%d) = %q, want %q", proto, got, want)
		}
	}
}

func mustAllow(t *testing.T, s string) []netip.Prefix {
	t.Helper()
	p, err := ParseAllow(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Intake is reported per interval, not as a running total. This matters more
// than it looks: on cumulative counters, a single malformed packet makes the
// collector log "undecodable: 1" every minute for the life of the process, and
// reports packets against an interval that drained cleanly long ago. The whole
// value of the line is telling an operator what is happening *now*.
func TestIntakeIsReportedPerIntervalNotCumulatively(t *testing.T) {
	c := &Collector{Agg: NewAggregator(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	from := &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 5000}
	good := v5Packet(t, 0, 0, Record{SrcAddr: addr("10.0.0.1"),
		DstAddr: addr("10.0.0.2"), InputIf: 1, Bytes: 100, Packets: 1})

	c.handle(good, from)
	c.handle([]byte{0, 5, 0, 30}, from) // undecodable
	if p, r, _, m := c.interval(); p != 1 || r != 1 || m != 1 {
		t.Fatalf("first interval: packets=%d records=%d malformed=%d, want 1/1/1", p, r, m)
	}

	// A quiet interval must report nothing, not repeat the previous one.
	if p, r, rf, m := c.interval(); p != 0 || r != 0 || rf != 0 || m != 0 {
		t.Errorf("quiet interval reported %d/%d/%d/%d, want zeroes", p, r, rf, m)
	}

	c.handle(good, from)
	if p, m := func() (uint64, uint64) { p, _, _, m := c.interval(); return p, m }(); p != 1 || m != 0 {
		t.Errorf("third interval: packets=%d malformed=%d, want 1/0", p, m)
	}

	// Running totals stay cumulative — the delta is a reporting view, not a
	// replacement for the counters themselves.
	if p, r, _, m := c.Stats(); p != 2 || r != 2 || m != 1 {
		t.Errorf("Stats totals = %d/%d/%d, want 2/2/1", p, r, m)
	}
}

// A service above the ephemeral floor must still be recognised. "Lower port
// wins" put a WireGuard flow (51820) under its client's port whenever that
// port happened to be lower — one bucket per connection, which is the exact
// cardinality blow-up the heuristic is there to avoid.
func TestApplicationPrefersRecognisedPortOverLowerPort(t *testing.T) {
	cases := []struct {
		name     string
		src, dst uint16
		proto    uint8
		want     string
	}{
		{"wireguard below client port", 28778, 51820, 17, "wireguard"},
		{"wireguard above client port", 51820, 58000, 17, "wireguard"},
		{"https from high client port", 51000, 443, 6, "https"},
		{"https as source", 443, 51000, 6, "https"},
		{"rdp above 1024", 3389, 60001, 6, "rdp"},
		{"neither recognised falls back to the lower port", 40000, 9418, 6, "tcp/9418"},
		{"both recognised keeps the lower", 443, 51820, 6, "https"},
		{"no ports at all", 0, 0, 1, "icmp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := application(Record{SrcPort: c.src, DstPort: c.dst, Protocol: c.proto})
			if got != c.want {
				t.Errorf("application(%d->%d proto %d) = %q, want %q",
					c.src, c.dst, c.proto, got, c.want)
			}
		})
	}
}

// The consequence that matters: a busy WireGuard tunnel collapses to one
// bucket instead of one per client port.
func TestWireguardDoesNotFanOutIntoOneBucketPerConnection(t *testing.T) {
	a := NewAggregator()
	recs := make([]Record, 0, 40)
	for i := 0; i < 40; i++ {
		recs = append(recs, Record{
			SrcAddr: addr("10.0.0.1"), DstAddr: addr("10.0.0.2"),
			SrcPort: uint16(20000 + i), DstPort: 51820, Protocol: 17,
			Bytes: 100, Packets: 1, InputIf: 1,
		})
	}
	a.Add(&Packet{ExporterIP: addr("192.0.2.1"), Records: recs})

	var apps int
	var total uint64
	for _, b := range a.Drain() {
		if b.Key.Dimension != DimApplication {
			continue
		}
		apps++
		total += b.Bytes
		if b.Key.Value != "wireguard" {
			t.Errorf("unexpected application bucket %q", b.Key.Value)
		}
	}
	if apps != 1 {
		t.Errorf("got %d application buckets, want 1", apps)
	}
	if total != 4000 {
		t.Errorf("bytes = %d, want 4000", total)
	}
}
