package flow

import (
	"encoding/binary"
	"testing"
	"time"
)

// The fields raw storage keeps (ADR-026). Each is optional in the export, so
// every test also checks the decoder leaves it zero when absent.

func TestV5TimestampsAndNextHop(t *testing.T) {
	b := v5Packet(t, 0, 0, Record{SrcAddr: addr("10.0.0.1"), DstAddr: addr("10.0.0.2"), InputIf: 1, Bytes: 1, Packets: 1})
	o := b[netflowV5HeaderLen:]
	copy(o[8:12], addr("10.0.0.254").AsSlice())
	binary.BigEndian.PutUint32(o[24:28], 400) // first: 600 ms before the header's uptime of 1000
	binary.BigEndian.PutUint32(o[28:32], 900)
	p, err := DecodeNetFlowV5(b, addr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	r := p.Records[0]
	export := time.Unix(1786000000, 0).UTC()
	if !r.Start.Equal(export.Add(-600*time.Millisecond)) || !r.End.Equal(export.Add(-100*time.Millisecond)) {
		t.Errorf("start %s end %s", r.Start, r.End)
	}
	if r.NextHop != addr("10.0.0.254") || r.Type != "netflow5" || r.SamplingRate != 1 {
		t.Errorf("next hop %s type %q rate %d", r.NextHop, r.Type, r.SamplingRate)
	}
}

func TestExportClockSurvivesTheUptimeWrap(t *testing.T) {
	export := time.Unix(1786000000, 0).UTC()
	// The header was stamped just after the 32-bit millisecond counter
	// wrapped; the flow started just before it.
	c := exportClock{uptimeMs: 500, unix: export}
	if got := c.at(0xFFFFFFFF - 499); !got.Equal(export.Add(-1000 * time.Millisecond)) {
		t.Fatalf("across the wrap: %s", got)
	}
	// A last-packet time a moment after the header's uptime is a small
	// negative age, not 49 days ago.
	if got := c.at(510); !got.Equal(export.Add(10 * time.Millisecond)) {
		t.Fatalf("slightly ahead: %s", got)
	}
	if !c.at(0).IsZero() {
		t.Fatal("an uptime of zero means not exported")
	}
}

func TestV9ReadsTheRawFields(t *testing.T) {
	tc := NewTemplateCache()
	fields := []field{
		{fSrcAddrV4, 4}, {fDstAddrV4, 4}, {fInBytes, 4}, {fInPkts, 4}, {fInputSNMP, 2},
		{fFirstSwitched, 4}, {fLastSwitched, 4}, {fInSrcMAC, 6}, {fInDstMAC, 6},
		{fSrcVLAN, 2}, {fDstVLAN, 2}, {fNextHopV4, 4}, {fDirection, 1}, {fICMPTypeV4, 2},
		{fApplicationID, 5}, {fNATSrcV4, 4}, {fNATSrcPort, 2}, {fFirewallEvent, 1}, {fASAUserName, 8},
	}
	rec := concat(
		addr("10.0.0.1").AsSlice(), addr("8.8.8.8").AsSlice(), be(1500, 4), be(3, 4), be(7, 2),
		be(400, 4), be(900, 4), be(0x001122334455, 6), be(0x66778899aabb, 6),
		be(0x1000|20, 2), be(30, 2), addr("10.0.0.254").AsSlice(), []byte{1}, be(8<<8, 2),
		[]byte{13, 0, 0, 0x01, 0xBB}, addr("203.0.113.9").AsSlice(), be(40000, 2), []byte{2},
		[]byte("alice\x00\x00\x00"),
	)
	pkt := concat(v9Header(2, 1), templateFlowSet(260, fields), dataFlowSet(260, rec))
	p, _, err := DecodeNetFlowV9(pkt, addr("192.0.2.1"), tc, time.Now())
	if err != nil || len(p.Records) != 1 {
		t.Fatalf("decode: %v, %d records", err, len(p.Records))
	}
	r := p.Records[0]
	export := time.Unix(1786000000, 0).UTC()
	checks := map[string]bool{
		"start":     r.Start.Equal(export.Add(-600 * time.Millisecond)),
		"end":       r.End.Equal(export.Add(-100 * time.Millisecond)),
		"src mac":   r.SrcMAC == 0x001122334455,
		"dst mac":   r.DstMAC == 0x66778899aabb,
		"src vlan":  r.SrcVLAN == 20, // priority bits above the 12-bit VLAN ID are masked off
		"dst vlan":  r.DstVLAN == 30,
		"next hop":  r.NextHop == addr("10.0.0.254"),
		"direction": r.Direction == 2, // wire 1 = egress, stored shifted so 0 means unknown
		"icmp":      r.ICMPType == 8<<8,
		"app id":    r.AppID == "13:443",
		"nat src":   r.NATSrcAddr == addr("203.0.113.9") && r.NATSrcPort == 40000,
		"fw event":  r.FWEvent == 2,
		"user":      r.User == "alice",
		"type":      r.Type == "netflow9",
		"rate":      r.SamplingRate == 1,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s wrong: %+v", name, r)
		}
	}
	if r.BGPNextHop.IsValid() || r.NATDstAddr.IsValid() || r.AppName != "" {
		t.Errorf("fields the template did not name must stay zero: %+v", r)
	}
}

func TestIPFIXAbsoluteAndBootRelativeTimes(t *testing.T) {
	tc := NewTemplateCache()
	start := time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)
	abs := []ipfixField{{fSrcAddrV4, 4, 0}, {fDstAddrV4, 4, 0}, {fInBytes, 8, 0}, {fInputSNMP, 4, 0},
		{fStartMillis, 8, 0}, {fEndMillis, 8, 0}, {fUserName, varLength, 0}}
	user := []byte("bob")
	rec := concat(addr("10.0.0.1").AsSlice(), addr("10.0.0.2").AsSlice(), be(10, 8), be(1, 4),
		be(uint64(start.UnixMilli()), 8), be(uint64(start.Add(2*time.Second).UnixMilli()), 8),
		append([]byte{byte(len(user))}, user...))
	p, _, err := DecodeIPFIX(ipfixMsg(1, ipfixTemplateSet(300, abs), wrapFlowSet(300, rec)), addr("192.0.2.1"), tc, time.Now())
	if err != nil || len(p.Records) != 1 {
		t.Fatalf("decode: %v", err)
	}
	if r := p.Records[0]; !r.Start.Equal(start) || !r.End.Equal(start.Add(2*time.Second)) || r.User != "bob" || r.Type != "ipfix" {
		t.Fatalf("absolute: %+v", r)
	}

	// Relative times count from systemInitTimeMilliseconds, which may come
	// after them in the template.
	rel := []ipfixField{{fSrcAddrV4, 4, 0}, {fDstAddrV4, 4, 0}, {fInBytes, 8, 0}, {fInputSNMP, 4, 0},
		{fFirstSwitched, 4, 0}, {fLastSwitched, 4, 0}, {fSysInitMillis, 8, 0}}
	boot := start.Add(-time.Hour)
	rec = concat(addr("10.0.0.1").AsSlice(), addr("10.0.0.2").AsSlice(), be(10, 8), be(1, 4),
		be(3_600_000, 4), be(3_601_000, 4), be(uint64(boot.UnixMilli()), 8))
	p, _, err = DecodeIPFIX(ipfixMsg(1, ipfixTemplateSet(301, rel), wrapFlowSet(301, rec)), addr("192.0.2.1"), tc, time.Now())
	if err != nil || len(p.Records) != 1 {
		t.Fatalf("decode: %v", err)
	}
	if r := p.Records[0]; !r.Start.Equal(start) || !r.End.Equal(start.Add(time.Second)) {
		t.Fatalf("relative: start %s end %s", r.Start, r.End)
	}
}

func TestInlineSamplingIsNotAppliedTwice(t *testing.T) {
	tc := NewTemplateCache()
	// The exporter announces 1-in-100 for the domain, and this record also
	// carries its own 1-in-10. The record's own rate wins; multiplying by
	// both would report 1000× the traffic.
	tc.putSampling(samplingKey{addr("192.0.2.1"), 1}, 100)
	fields := []field{{fSrcAddrV4, 4}, {fDstAddrV4, 4}, {fInBytes, 4}, {fInputSNMP, 2}, {fSamplingInterval, 4}}
	rec := concat(addr("10.0.0.1").AsSlice(), addr("10.0.0.2").AsSlice(), be(50, 4), be(1, 2), be(10, 4))
	p, _, err := DecodeNetFlowV9(concat(v9Header(2, 1), templateFlowSet(270, fields), dataFlowSet(270, rec)),
		addr("192.0.2.1"), tc, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Records[0]; r.Bytes != 500 || r.SamplingRate != 10 {
		t.Fatalf("bytes %d rate %d, want 500 and 10", r.Bytes, r.SamplingRate)
	}
}

func TestTextFieldsAreBoundedAndValid(t *testing.T) {
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	if got := text(long); len(got) != 128 {
		t.Fatalf("length %d, want 128", len(got))
	}
	if got := text([]byte{'o', 'k', 0xff, 0xfe}); got != "ok" {
		t.Fatalf("invalid UTF-8 kept: %q", got)
	}
	if got := appID([]byte{3, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a}); got != "3:0102030405060708090a" {
		t.Fatalf("long selector: %q", got)
	}
	if addrOrZero(make([]byte, 4)).IsValid() || addrOrZero([]byte{1, 2, 3}).IsValid() {
		t.Fatal("the unspecified address and a malformed one must both read as none")
	}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
