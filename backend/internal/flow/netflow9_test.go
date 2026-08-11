package flow

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

// --- wire-format builders -------------------------------------------------
//
// As with v5, packets are assembled byte by byte so the decoder is tested
// against the format rather than against a struct someone typed twice. v9
// makes this more valuable, not less: almost every way to get v9 wrong is an
// offset or a length, and a builder sharing the decoder's assumptions would
// agree with it while both were wrong.

func v9Header(count uint16, sourceID uint32) []byte {
	b := make([]byte, netflowV9HeaderLen)
	binary.BigEndian.PutUint16(b[0:2], 9)
	binary.BigEndian.PutUint16(b[2:4], count)
	binary.BigEndian.PutUint32(b[4:8], 1000)        // sysUptime
	binary.BigEndian.PutUint32(b[8:12], 1786000000) // unix secs
	binary.BigEndian.PutUint32(b[12:16], 1)         // sequence
	binary.BigEndian.PutUint32(b[16:20], sourceID)
	return b
}

type field struct{ typ, length uint16 }

// templateFlowSet builds a FlowSet 0 announcing one template.
func templateFlowSet(id uint16, fields []field) []byte {
	body := make([]byte, 0, 4+len(fields)*4)
	body = binary.BigEndian.AppendUint16(body, id)
	body = binary.BigEndian.AppendUint16(body, uint16(len(fields)))
	for _, f := range fields {
		body = binary.BigEndian.AppendUint16(body, f.typ)
		body = binary.BigEndian.AppendUint16(body, f.length)
	}
	return wrapFlowSet(fsIDTemplate, body)
}

// optionsTemplateFlowSet builds a FlowSet 1. The two lengths are in *bytes of
// field definitions*, not counts of fields — the single most reliable place to
// go wrong in v9, since every other count in the format is a field count.
func optionsTemplateFlowSet(id uint16, scope, options []field) []byte {
	body := make([]byte, 0, 6+(len(scope)+len(options))*4)
	body = binary.BigEndian.AppendUint16(body, id)
	body = binary.BigEndian.AppendUint16(body, uint16(len(scope)*4))
	body = binary.BigEndian.AppendUint16(body, uint16(len(options)*4))
	for _, f := range append(append([]field{}, scope...), options...) {
		body = binary.BigEndian.AppendUint16(body, f.typ)
		body = binary.BigEndian.AppendUint16(body, f.length)
	}
	return wrapFlowSet(fsIDOptionsTemplate, body)
}

func dataFlowSet(templateID uint16, records ...[]byte) []byte {
	var body []byte
	for _, r := range records {
		body = append(body, r...)
	}
	return wrapFlowSet(templateID, body)
}

func wrapFlowSet(id uint16, body []byte) []byte {
	out := make([]byte, 0, flowsetHeaderLen+len(body))
	out = binary.BigEndian.AppendUint16(out, id)
	out = binary.BigEndian.AppendUint16(out, uint16(flowsetHeaderLen+len(body)))
	return append(out, body...)
}

func be(v uint64, width int) []byte {
	b := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}

// The layout a typical exporter announces for IPv4 flows.
var v4Fields = []field{
	{fSrcAddrV4, 4}, {fDstAddrV4, 4},
	{fInputSNMP, 2}, {fOutputSNMP, 2},
	{fInBytes, 4}, {fInPkts, 4},
	{fSrcPort, 2}, {fDstPort, 2}, {fProtocol, 1},
}

func v4Record(src, dst string, in, out uint16, bytes, pkts uint64, sport, dport uint16, proto uint8) []byte {
	var r []byte
	r = append(r, netip.MustParseAddr(src).AsSlice()...)
	r = append(r, netip.MustParseAddr(dst).AsSlice()...)
	r = append(r, be(uint64(in), 2)...)
	r = append(r, be(uint64(out), 2)...)
	r = append(r, be(bytes, 4)...)
	r = append(r, be(pkts, 4)...)
	r = append(r, be(uint64(sport), 2)...)
	r = append(r, be(uint64(dport), 2)...)
	return append(r, proto)
}

// --- tests ----------------------------------------------------------------

func TestV9TemplateThenDataDecodes(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	from := addr("192.0.2.1")

	pkt := append(v9Header(2, 7), templateFlowSet(300, v4Fields)...)
	pkt = append(pkt, dataFlowSet(300,
		v4Record("10.0.0.1", "10.0.0.2", 3, 4, 1500, 10, 51000, 443, 6))...)

	p, awaiting, err := DecodeNetFlowV9(pkt, from, tc, now)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if awaiting != 0 {
		t.Errorf("awaiting = %d, want 0 — the template was in the same packet", awaiting)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(p.Records))
	}
	r := p.Records[0]
	if r.SrcAddr != addr("10.0.0.1") || r.DstAddr != addr("10.0.0.2") {
		t.Errorf("addresses = %v -> %v", r.SrcAddr, r.DstAddr)
	}
	if r.Bytes != 1500 || r.Packets != 10 {
		t.Errorf("counters = %d bytes / %d packets", r.Bytes, r.Packets)
	}
	if r.InputIf != 3 || r.OutputIf != 4 {
		t.Errorf("ifIndexes = %d / %d", r.InputIf, r.OutputIf)
	}
	if r.SrcPort != 51000 || r.DstPort != 443 || r.Protocol != 6 {
		t.Errorf("ports/proto = %d -> %d / %d", r.SrcPort, r.DstPort, r.Protocol)
	}
	if r.Sampled {
		t.Error("an unsampled export was marked sampled")
	}
}

// Data ahead of its template is the normal state after a restart, not a fault.
// It must be reported distinctly so an operator is not sent hunting.
func TestV9DataBeforeItsTemplateIsCountedNotFailed(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	from := addr("192.0.2.1")

	orphan := append(v9Header(1, 7), dataFlowSet(300,
		v4Record("10.0.0.1", "10.0.0.2", 3, 4, 1500, 10, 51000, 443, 6))...)
	p, awaiting, err := DecodeNetFlowV9(orphan, from, tc, now)
	if err != nil {
		t.Fatalf("orphan data must not be an error: %v", err)
	}
	if awaiting != 1 {
		t.Errorf("awaiting = %d, want 1", awaiting)
	}
	if len(p.Records) != 0 {
		t.Errorf("got %d records without a template", len(p.Records))
	}

	// Once the template arrives, the next data packet decodes.
	tmpl := append(v9Header(1, 7), templateFlowSet(300, v4Fields)...)
	if _, _, err := DecodeNetFlowV9(tmpl, from, tc, now); err != nil {
		t.Fatal(err)
	}
	p, awaiting, err = DecodeNetFlowV9(orphan, from, tc, now)
	if err != nil || awaiting != 0 || len(p.Records) != 1 {
		t.Errorf("after the template: %d records, awaiting %d, err %v",
			len(p.Records), awaiting, err)
	}
}

// A packet carrying templates *and* data must yield the data it can read. All
// or nothing would throw away usable flows during the post-restart window.
func TestV9MixedPacketYieldsWhatItCan(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	pkt := append(v9Header(3, 7), templateFlowSet(300, v4Fields)...)
	pkt = append(pkt, dataFlowSet(300,
		v4Record("10.0.0.1", "10.0.0.2", 1, 2, 100, 1, 1, 443, 6))...)
	pkt = append(pkt, dataFlowSet(999, make([]byte, 8))...) // template never sent

	p, awaiting, err := DecodeNetFlowV9(pkt, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Errorf("records = %d, want the one whose template was present", len(p.Records))
	}
	if awaiting != 1 {
		t.Errorf("awaiting = %d, want 1 for the unknown template", awaiting)
	}
}

// Field widths are the exporter's choice: INPUT_SNMP is 2 bytes on one
// platform and 4 on another. Assuming a width silently reads the wrong bytes.
func TestV9HonoursDeclaredFieldWidths(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	wide := []field{
		{fSrcAddrV4, 4}, {fDstAddrV4, 4},
		{fInputSNMP, 4}, {fOutputSNMP, 4},
		{fInBytes, 8}, {fInPkts, 8},
		{fProtocol, 1},
	}
	var rec []byte
	rec = append(rec, netip.MustParseAddr("10.0.0.1").AsSlice()...)
	rec = append(rec, netip.MustParseAddr("10.0.0.2").AsSlice()...)
	rec = append(rec, be(70000, 4)...) // an ifIndex that does not fit in 16 bits
	rec = append(rec, be(4, 4)...)
	rec = append(rec, be(5_000_000_000, 8)...) // and a byte count that does not fit in 32
	rec = append(rec, be(9, 8)...)
	rec = append(rec, 17)

	pkt := append(v9Header(2, 7), templateFlowSet(301, wide)...)
	pkt = append(pkt, dataFlowSet(301, rec)...)

	p, _, err := DecodeNetFlowV9(pkt, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d", len(p.Records))
	}
	if got := p.Records[0].InputIf; got != 70000 {
		t.Errorf("InputIf = %d, want 70000", got)
	}
	if got := p.Records[0].Bytes; got != 5_000_000_000 {
		t.Errorf("Bytes = %d, want 5000000000", got)
	}
}

// v9 carries IPv6, which v5 cannot express at all. This is the reason a
// dual-stack link is only half-reported under v5.
func TestV9DecodesIPv6Flows(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	v6 := []field{
		{fSrcAddrV6, 16}, {fDstAddrV6, 16},
		{fInputSNMP, 2}, {fInBytes, 4}, {fInPkts, 4},
		{fSrcPort, 2}, {fDstPort, 2}, {fProtocol, 1},
	}
	var rec []byte
	rec = append(rec, netip.MustParseAddr("2001:db8::1").AsSlice()...)
	rec = append(rec, netip.MustParseAddr("2001:db8::2").AsSlice()...)
	rec = append(rec, be(3, 2)...)
	rec = append(rec, be(2000, 4)...)
	rec = append(rec, be(12, 4)...)
	rec = append(rec, be(51000, 2)...)
	rec = append(rec, be(443, 2)...)
	rec = append(rec, 6)

	pkt := append(v9Header(2, 7), templateFlowSet(302, v6)...)
	pkt = append(pkt, dataFlowSet(302, rec)...)

	p, _, err := DecodeNetFlowV9(pkt, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d", len(p.Records))
	}
	if got := p.Records[0].SrcAddr; got != netip.MustParseAddr("2001:db8::1") {
		t.Errorf("v6 source = %v", got)
	}
	// And it must survive aggregation, where addresses become label values.
	a := NewAggregator()
	a.Add(p)
	var sawV6 bool
	for _, b := range a.Drain() {
		if b.Key.Dimension == DimTalker && b.Key.Value == "2001:db8::1" {
			sawV6 = true
		}
	}
	if !sawV6 {
		t.Error("the IPv6 talker did not survive aggregation")
	}
}

// Sampling usually arrives in an options data record describing the sampler,
// not on the flow itself. Ignoring it under-reports by the sampling rate with
// no error anywhere — the same silent wrongness the v5 path guards against,
// reached by a different route.
func TestV9AppliesSamplingFromAnOptionsRecord(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	from := addr("192.0.2.1")

	// Options template: one scope field (a system id), one sampling interval.
	optT := optionsTemplateFlowSet(400,
		[]field{{1, 4}},                 // scope: system
		[]field{{fSamplingInterval, 4}}, // option: sampling interval
	)
	optData := dataFlowSet(400, append(be(1, 4), be(100, 4)...))

	pkt := append(v9Header(4, 7), templateFlowSet(300, v4Fields)...)
	pkt = append(pkt, optT...)
	pkt = append(pkt, optData...)
	pkt = append(pkt, dataFlowSet(300,
		v4Record("10.0.0.1", "10.0.0.2", 3, 4, 1500, 10, 51000, 443, 6))...)

	p, _, err := DecodeNetFlowV9(pkt, from, tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d", len(p.Records))
	}
	r := p.Records[0]
	if r.Bytes != 1500*100 {
		t.Errorf("Bytes = %d, want the sampled count scaled by 100", r.Bytes)
	}
	if r.Packets != 10*100 {
		t.Errorf("Packets = %d, want scaling by 100", r.Packets)
	}
	if !r.Sampled {
		t.Error("a sampled record was not marked as an estimate")
	}
}

// An options record must not become a flow. Its fields are a sampler
// description, and reading it as traffic invents flows from configuration.
func TestV9OptionsDataIsNotTreatedAsFlow(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	pkt := append(v9Header(2, 7),
		optionsTemplateFlowSet(400, []field{{1, 4}}, []field{{fSamplingInterval, 4}})...)
	pkt = append(pkt, dataFlowSet(400, append(be(1, 4), be(10, 4)...))...)

	p, _, err := DecodeNetFlowV9(pkt, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 0 {
		t.Errorf("an options record produced %d flow records", len(p.Records))
	}
}

// A redefined template must take effect: exporters reuse IDs after a
// configuration change, and holding the old layout would misread every
// subsequent record while looking perfectly healthy.
func TestV9TemplateRedefinitionTakesEffect(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	from := addr("192.0.2.1")

	first := append(v9Header(1, 7), templateFlowSet(300, v4Fields)...)
	if _, _, err := DecodeNetFlowV9(first, from, tc, now); err != nil {
		t.Fatal(err)
	}

	// Same ID, different layout: no ports, wider counters.
	redefined := []field{
		{fSrcAddrV4, 4}, {fDstAddrV4, 4}, {fInputSNMP, 2},
		{fInBytes, 4}, {fInPkts, 4}, {fProtocol, 1},
	}
	var rec []byte
	rec = append(rec, netip.MustParseAddr("10.1.1.1").AsSlice()...)
	rec = append(rec, netip.MustParseAddr("10.1.1.2").AsSlice()...)
	rec = append(rec, be(9, 2)...)
	rec = append(rec, be(4242, 4)...)
	rec = append(rec, be(7, 4)...)
	rec = append(rec, 17)

	pkt := append(v9Header(2, 7), templateFlowSet(300, redefined)...)
	pkt = append(pkt, dataFlowSet(300, rec)...)
	p, _, err := DecodeNetFlowV9(pkt, from, tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d", len(p.Records))
	}
	if got := p.Records[0].Bytes; got != 4242 {
		t.Errorf("Bytes = %d — the old layout was still in use", got)
	}
	if got := p.Records[0].InputIf; got != 9 {
		t.Errorf("InputIf = %d", got)
	}
}

// Templates are attacker-influenced state on an unauthenticated port. The
// cache must refuse to grow without bound rather than track every ID a spoofed
// source cares to mint.
func TestV9TemplateCacheIsBounded(t *testing.T) {
	tc := NewTemplateCache()
	tc.Max = 4
	now := time.Now()
	for i := 0; i < 50; i++ {
		pkt := append(v9Header(1, uint32(i)), templateFlowSet(300, v4Fields)...)
		if _, _, err := DecodeNetFlowV9(pkt, addr("192.0.2.1"), tc, now); err != nil {
			t.Fatal(err)
		}
	}
	if n := tc.Len(); n > 4 {
		t.Errorf("cache holds %d templates, cap is 4", n)
	}
}

// A template not refreshed within the TTL is forgotten, so a decommissioned
// exporter's layouts do not sit in memory indefinitely.
func TestV9TemplatesExpire(t *testing.T) {
	tc := NewTemplateCache()
	tc.TTL = time.Minute
	start := time.Now()
	from := addr("192.0.2.1")

	tmpl := append(v9Header(1, 7), templateFlowSet(300, v4Fields)...)
	if _, _, err := DecodeNetFlowV9(tmpl, from, tc, start); err != nil {
		t.Fatal(err)
	}
	data := append(v9Header(1, 7), dataFlowSet(300,
		v4Record("10.0.0.1", "10.0.0.2", 3, 4, 1500, 10, 51000, 443, 6))...)

	if p, _, _ := DecodeNetFlowV9(data, from, tc, start.Add(30*time.Second)); len(p.Records) != 1 {
		t.Error("a live template was not used")
	}
	p, awaiting, _ := DecodeNetFlowV9(data, from, tc, start.Add(2*time.Minute))
	if len(p.Records) != 0 || awaiting != 1 {
		t.Errorf("an expired template was still used: %d records, awaiting %d",
			len(p.Records), awaiting)
	}
}

// Lengths inside the packet are attacker-controlled and must never send the
// decoder past the buffer or into a loop that does not advance.
func TestV9MalformedPacketsAreRejectedNotFatal(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	cases := map[string][]byte{
		"short header":       {0, 9, 0, 1},
		"wrong version":      append(v9Header(1, 7)[:0:0], append([]byte{0, 5}, make([]byte, 18)...)...),
		"flowset overruns":   append(v9Header(1, 7), 0, 0, 255, 255),
		"zero-length set":    append(v9Header(1, 7), 0, 0, 0, 0),
		"truncated template": append(v9Header(1, 7), 0, 0, 0, 8, 1, 44, 0, 99),
	}
	for name, pkt := range cases {
		t.Run(name, func(t *testing.T) {
			// Must return, not panic, and not hang.
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _, _ = DecodeNetFlowV9(pkt, addr("192.0.2.1"), tc, now)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("decode did not terminate")
			}
		})
	}
}

// Field widths are the exporter's choice, so a template can declare an 8-byte
// port. Keeping the low bytes would produce a plausible wrong port from data
// arriving on an unauthenticated socket; a value that cannot fit is a
// misdescribed field, not a large port.
func TestV9OversizedFieldsBecomeUnknownRatherThanTruncated(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	fields := []field{
		{fSrcAddrV4, 4}, {fDstAddrV4, 4},
		{fInputSNMP, 8}, {fSrcPort, 8}, {fInBytes, 4}, {fProtocol, 1},
	}
	var rec []byte
	rec = append(rec, netip.MustParseAddr("10.0.0.1").AsSlice()...)
	rec = append(rec, netip.MustParseAddr("10.0.0.2").AsSlice()...)
	rec = append(rec, be(0x1_0000_0000_0001, 8)...) // ifIndex beyond uint32
	rec = append(rec, be(0x1_0001, 8)...)           // port beyond uint16
	rec = append(rec, be(500, 4)...)
	rec = append(rec, 6)

	pkt := append(v9Header(2, 7), templateFlowSet(310, fields)...)
	pkt = append(pkt, dataFlowSet(310, rec)...)
	p, _, err := DecodeNetFlowV9(pkt, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d", len(p.Records))
	}
	if got := p.Records[0].SrcPort; got != 0 {
		t.Errorf("SrcPort = %d, want 0 — 0x10001 truncated to 1 would look real", got)
	}
	if got := p.Records[0].InputIf; got != 0 {
		t.Errorf("InputIf = %d, want 0", got)
	}
	// And an unattributable flow is then dropped by the aggregator rather than
	// piling onto ifIndex 0.
	a := NewAggregator()
	a.Add(p)
	if got := a.Drain(); len(got) != 0 {
		t.Errorf("an unattributable flow produced %d buckets", len(got))
	}
}
