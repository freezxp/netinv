package flow

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

// --- wire-format builders -------------------------------------------------

// ipfixMsg wraps sets in a header, filling in the total length — IPFIX's header
// carries a byte count where v9's carries a record count, and getting that
// wrong is one of the two silent ways to misread the format.
func ipfixMsg(domainID uint32, sets ...[]byte) []byte {
	var body []byte
	for _, s := range sets {
		body = append(body, s...)
	}
	b := make([]byte, ipfixHeaderLen)
	binary.BigEndian.PutUint16(b[0:2], 10)
	binary.BigEndian.PutUint16(b[2:4], uint16(ipfixHeaderLen+len(body)))
	binary.BigEndian.PutUint32(b[4:8], 1786000000)
	binary.BigEndian.PutUint32(b[8:12], 1)
	binary.BigEndian.PutUint32(b[12:16], domainID)
	return append(b, body...)
}

type ipfixField struct {
	typ        uint16
	length     uint16
	enterprise uint32 // non-zero renders the enterprise form
}

func ipfixTemplateSet(id uint16, fields []ipfixField) []byte {
	body := binary.BigEndian.AppendUint16(nil, id)
	body = binary.BigEndian.AppendUint16(body, uint16(len(fields)))
	body = append(body, encodeFields(fields)...)
	return wrapFlowSet(ipfixSetTemplate, body)
}

// ipfixOptionsTemplateSet declares a scope field *count*, where v9 declares
// scope and option byte *lengths* — same size field, same position, different
// unit.
func ipfixOptionsTemplateSet(id uint16, scope, options []ipfixField) []byte {
	all := append(append([]ipfixField{}, scope...), options...)
	body := binary.BigEndian.AppendUint16(nil, id)
	body = binary.BigEndian.AppendUint16(body, uint16(len(all)))
	body = binary.BigEndian.AppendUint16(body, uint16(len(scope)))
	body = append(body, encodeFields(all)...)
	return wrapFlowSet(ipfixSetOptionsTmpl, body)
}

func encodeFields(fields []ipfixField) []byte {
	var out []byte
	for _, f := range fields {
		id := f.typ
		if f.enterprise != 0 {
			id |= enterpriseBit
		}
		out = binary.BigEndian.AppendUint16(out, id)
		out = binary.BigEndian.AppendUint16(out, f.length)
		if f.enterprise != 0 {
			out = binary.BigEndian.AppendUint32(out, f.enterprise)
		}
	}
	return out
}

var ipfixV4Fields = []ipfixField{
	{typ: fSrcAddrV4, length: 4}, {typ: fDstAddrV4, length: 4},
	{typ: fInputSNMP, length: 4}, {typ: fOutputSNMP, length: 4},
	{typ: fInBytes, length: 8}, {typ: fInPkts, length: 8},
	{typ: fSrcPort, length: 2}, {typ: fDstPort, length: 2},
	{typ: fProtocol, length: 1},
}

func ipfixV4Record(src, dst string, in, out uint32, bytes, pkts uint64, sport, dport uint16, proto uint8) []byte {
	var r []byte
	r = append(r, netip.MustParseAddr(src).AsSlice()...)
	r = append(r, netip.MustParseAddr(dst).AsSlice()...)
	r = append(r, be(uint64(in), 4)...)
	r = append(r, be(uint64(out), 4)...)
	r = append(r, be(bytes, 8)...)
	r = append(r, be(pkts, 8)...)
	r = append(r, be(uint64(sport), 2)...)
	r = append(r, be(uint64(dport), 2)...)
	return append(r, proto)
}

// --- tests ----------------------------------------------------------------

func TestIPFIXTemplateThenDataDecodes(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	msg := ipfixMsg(9,
		ipfixTemplateSet(256, ipfixV4Fields),
		wrapFlowSet(256, ipfixV4Record("10.0.0.1", "10.0.0.2", 3, 4, 1500, 10, 51000, 443, 6)),
	)
	p, awaiting, err := DecodeIPFIX(msg, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if awaiting != 0 {
		t.Errorf("awaiting = %d, want 0", awaiting)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d", len(p.Records))
	}
	r := p.Records[0]
	if r.SrcAddr != addr("10.0.0.1") || r.Bytes != 1500 || r.Packets != 10 ||
		r.InputIf != 3 || r.SrcPort != 51000 || r.DstPort != 443 || r.Protocol != 6 {
		t.Errorf("decoded record wrong: %+v", r)
	}
}

// The header carries a byte length, not a record count. A message padded by a
// middlebox must be read to its declared length, and one claiming more than
// arrived must be rejected rather than read past the buffer.
func TestIPFIXHonoursItsDeclaredLength(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	msg := ipfixMsg(9,
		ipfixTemplateSet(256, ipfixV4Fields),
		wrapFlowSet(256, ipfixV4Record("10.0.0.1", "10.0.0.2", 1, 2, 100, 1, 1, 443, 6)),
	)

	// Trailing bytes beyond the declared length must be ignored, not parsed.
	padded := append(append([]byte{}, msg...), make([]byte, 32)...)
	p, _, err := DecodeIPFIX(padded, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatalf("padded message: %v", err)
	}
	if len(p.Records) != 1 {
		t.Errorf("padded message gave %d records, want 1", len(p.Records))
	}

	// A length longer than the datagram is a truncated message, not a licence
	// to read on.
	lying := append([]byte{}, msg...)
	binary.BigEndian.PutUint16(lying[2:4], uint16(len(msg)+64))
	if _, _, err := DecodeIPFIX(lying, addr("192.0.2.1"), tc, now); err == nil {
		t.Error("a message claiming more bytes than arrived must be rejected")
	}
}

// The high bit of an element ID means a 4-byte enterprise number follows in
// the template. Missing it offsets every later field by four bytes, which
// still parses and produces confident nonsense.
func TestIPFIXEnterpriseFieldsDoNotShiftLaterFields(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	fields := []ipfixField{
		{typ: fSrcAddrV4, length: 4}, {typ: fDstAddrV4, length: 4},
		{typ: 1234, length: 4, enterprise: 9999}, // vendor-private, must be skipped
		{typ: fInputSNMP, length: 4},
		{typ: fInBytes, length: 8},
		{typ: fProtocol, length: 1},
	}
	var rec []byte
	rec = append(rec, netip.MustParseAddr("10.0.0.1").AsSlice()...)
	rec = append(rec, netip.MustParseAddr("10.0.0.2").AsSlice()...)
	rec = append(rec, be(0xDEADBEEF, 4)...) // the vendor field
	rec = append(rec, be(7, 4)...)
	rec = append(rec, be(4242, 8)...)
	rec = append(rec, 17)

	msg := ipfixMsg(9, ipfixTemplateSet(256, fields), wrapFlowSet(256, rec))
	p, _, err := DecodeIPFIX(msg, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d", len(p.Records))
	}
	if got := p.Records[0].InputIf; got != 7 {
		t.Errorf("InputIf = %d, want 7 — the enterprise number shifted the fields", got)
	}
	if got := p.Records[0].Bytes; got != 4242 {
		t.Errorf("Bytes = %d, want 4242", got)
	}
	if got := p.Records[0].Protocol; got != 17 {
		t.Errorf("Protocol = %d, want 17", got)
	}
}

// A variable-length field carries its width in the record. A decoder that
// assumed the template's 0xFFFF was a real width would read 65535 bytes.
func TestIPFIXVariableLengthFieldsAreWalked(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	fields := []ipfixField{
		{typ: fSrcAddrV4, length: 4}, {typ: fDstAddrV4, length: 4},
		{typ: 82, length: varLength}, // interfaceName, variable
		{typ: fInputSNMP, length: 4},
		{typ: fInBytes, length: 8},
		{typ: fProtocol, length: 1},
	}
	rec := func(name string, in uint32, bytes uint64) []byte {
		var r []byte
		r = append(r, netip.MustParseAddr("10.0.0.1").AsSlice()...)
		r = append(r, netip.MustParseAddr("10.0.0.2").AsSlice()...)
		r = append(r, byte(len(name)))
		r = append(r, name...)
		r = append(r, be(uint64(in), 4)...)
		r = append(r, be(bytes, 8)...)
		return append(r, 6)
	}
	// Two records of different physical widths, which is the whole point.
	msg := ipfixMsg(9, ipfixTemplateSet(256, fields),
		wrapFlowSet(256, append(rec("eth0", 3, 1000), rec("GigabitEthernet0/0/1", 9, 2000)...)))

	p, _, err := DecodeIPFIX(msg, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(p.Records))
	}
	if p.Records[0].InputIf != 3 || p.Records[0].Bytes != 1000 {
		t.Errorf("first record: %+v", p.Records[0])
	}
	if p.Records[1].InputIf != 9 || p.Records[1].Bytes != 2000 {
		t.Errorf("second record misaligned after a longer variable field: %+v", p.Records[1])
	}
}

// The three-byte form: 255 followed by a 16-bit length, for values 255 bytes
// or longer.
func TestIPFIXLongVariableLengthEncoding(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	fields := []ipfixField{
		{typ: fSrcAddrV4, length: 4}, {typ: fDstAddrV4, length: 4},
		{typ: 82, length: varLength},
		{typ: fInBytes, length: 4},
		{typ: fInputSNMP, length: 2},
	}
	long := make([]byte, 300)
	var r []byte
	r = append(r, netip.MustParseAddr("10.0.0.1").AsSlice()...)
	r = append(r, netip.MustParseAddr("10.0.0.2").AsSlice()...)
	r = append(r, 255)
	r = binary.BigEndian.AppendUint16(r, uint16(len(long)))
	r = append(r, long...)
	r = append(r, be(777, 4)...)
	r = append(r, be(4, 2)...)

	msg := ipfixMsg(9, ipfixTemplateSet(256, fields), wrapFlowSet(256, r))
	p, _, err := DecodeIPFIX(msg, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d", len(p.Records))
	}
	if p.Records[0].Bytes != 777 || p.Records[0].InputIf != 4 {
		t.Errorf("fields after a 300-byte variable value are misaligned: %+v", p.Records[0])
	}
}

// Options templates declare a scope field count, not a byte length. Reading it
// as v9 does would treat the count as bytes and mis-split scope from options.
func TestIPFIXOptionsSamplingIsApplied(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	from := addr("192.0.2.1")

	optT := ipfixOptionsTemplateSet(258,
		[]ipfixField{{typ: 149, length: 4}},              // scope: observationDomainId
		[]ipfixField{{typ: fSamplerInterval, length: 4}}, // samplingPacketInterval
	)
	optData := wrapFlowSet(258, append(be(9, 4), be(64, 4)...))

	msg := ipfixMsg(9,
		ipfixTemplateSet(256, ipfixV4Fields), optT, optData,
		wrapFlowSet(256, ipfixV4Record("10.0.0.1", "10.0.0.2", 3, 4, 1500, 10, 51000, 443, 6)),
	)
	p, _, err := DecodeIPFIX(msg, from, tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d", len(p.Records))
	}
	if got := p.Records[0].Bytes; got != 1500*64 {
		t.Errorf("Bytes = %d, want the count scaled by the announced 1:64", got)
	}
	if !p.Records[0].Sampled {
		t.Error("a sampled record was not marked as an estimate")
	}
}

func TestIPFIXOptionsDataIsNotTreatedAsFlow(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	msg := ipfixMsg(9,
		ipfixOptionsTemplateSet(258,
			[]ipfixField{{typ: 149, length: 4}},
			[]ipfixField{{typ: fSamplerInterval, length: 4}}),
		wrapFlowSet(258, append(be(9, 4), be(8, 4)...)),
	)
	p, _, err := DecodeIPFIX(msg, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 0 {
		t.Errorf("an options record produced %d flow records", len(p.Records))
	}
}

// v9 and IPFIX number their templates independently. An exporter running both
// can use ID 256 for two different layouts, and sharing a cache key would let
// each overwrite the other.
func TestIPFIXAndV9TemplateSpacesDoNotCollide(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	from := addr("192.0.2.1")

	// Same exporter, same domain/source id, same template id, different layouts.
	v9pkt := append(v9Header(1, 9), templateFlowSet(256, v4Fields)...)
	if _, _, err := DecodeNetFlowV9(v9pkt, from, tc, now); err != nil {
		t.Fatal(err)
	}
	ipfixPkt := ipfixMsg(9, ipfixTemplateSet(256, ipfixV4Fields))
	if _, _, err := DecodeIPFIX(ipfixPkt, from, tc, now); err != nil {
		t.Fatal(err)
	}
	if n := tc.Len(); n != 2 {
		t.Fatalf("cache holds %d templates, want 2 — the versions collided", n)
	}

	// Each must still decode with its own layout: the v9 record is 25 bytes
	// wide, the IPFIX one 37, so a collision would misread both.
	v9data := append(v9Header(1, 9), dataFlowSet(256,
		v4Record("10.0.0.1", "10.0.0.2", 3, 4, 1500, 10, 51000, 443, 6))...)
	p, _, _ := DecodeNetFlowV9(v9data, from, tc, now)
	if len(p.Records) != 1 || p.Records[0].Bytes != 1500 {
		t.Errorf("v9 record misread after IPFIX registered the same id: %+v", p.Records)
	}
	ipfixData := ipfixMsg(9, wrapFlowSet(256,
		ipfixV4Record("10.1.1.1", "10.1.1.2", 7, 8, 999, 3, 40000, 22, 6)))
	p, _, _ = DecodeIPFIX(ipfixData, from, tc, now)
	if len(p.Records) != 1 || p.Records[0].Bytes != 999 {
		t.Errorf("IPFIX record misread: %+v", p.Records)
	}
}

func TestIPFIXDataBeforeItsTemplateIsCounted(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	msg := ipfixMsg(9, wrapFlowSet(256, make([]byte, 37)))
	p, awaiting, err := DecodeIPFIX(msg, addr("192.0.2.1"), tc, now)
	if err != nil {
		t.Fatal(err)
	}
	if awaiting != 1 || len(p.Records) != 0 {
		t.Errorf("awaiting = %d, records = %d; want 1 and 0", awaiting, len(p.Records))
	}
}

func TestIPFIXMalformedMessagesTerminate(t *testing.T) {
	tc := NewTemplateCache()
	now := time.Now()
	cases := map[string][]byte{
		"short header":     {0, 10, 0, 4},
		"wrong version":    append([]byte{0, 9}, make([]byte, 18)...),
		"length too small": {0, 10, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		"set overruns":     append(ipfixMsg(9), 0, 0, 255, 255),
		"zero-length set":  append(ipfixMsg(9), 1, 0, 0, 0),
		"truncated tmpl":   ipfixMsg(9, wrapFlowSet(ipfixSetTemplate, []byte{1, 0, 0, 3, 0, 8})),
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _, _ = DecodeIPFIX(msg, addr("192.0.2.1"), tc, now)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("decode did not terminate")
			}
		})
	}
}
