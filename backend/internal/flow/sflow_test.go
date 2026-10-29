package flow

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// xdrBuf builds sFlow datagrams for tests: every value 4-byte aligned, every
// variable part length-prefixed, as the agent would send it.
type xdrBuf struct{ b []byte }

func (w *xdrBuf) u32(vs ...uint32) *xdrBuf {
	for _, v := range vs {
		w.b = binary.BigEndian.AppendUint32(w.b, v)
	}
	return w
}

// fixed appends bytes padded to a 4-byte boundary, with no length prefix.
func (w *xdrBuf) fixed(b []byte) *xdrBuf {
	w.b = append(w.b, b...)
	for len(w.b)%4 != 0 {
		w.b = append(w.b, 0)
	}
	return w
}

// opaque appends a length-prefixed, padded byte string.
func (w *xdrBuf) opaque(b []byte) *xdrBuf {
	return w.u32(uint32(len(b))).fixed(b)
}

// item appends a format word and a length-prefixed body: the shape of both a
// sample and a flow record.
func (w *xdrBuf) item(format uint32, body []byte) *xdrBuf {
	return w.u32(format).opaque(body)
}

func sflowDatagram(samples ...func(*xdrBuf)) []byte {
	w := &xdrBuf{}
	// version, agent IPv4 203.0.113.50, sub-agent, sequence, uptime
	w.u32(5, 1).fixed([]byte{203, 0, 113, 50}).u32(0, 42, 123456)
	w.u32(uint32(len(samples)))
	for _, s := range samples {
		s(w)
	}
	return w.b
}

// ethTCP builds an Ethernet II frame with one 802.1Q tag carrying an IPv4
// TCP segment.
func ethTCP() []byte {
	f := []byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, // dst MAC
		0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, // src MAC
		0x81, 0x00, 0x00, 0x64, // 802.1Q, VLAN 100
		0x08, 0x00, // IPv4
	}
	ip := []byte{
		0x45, 0xB8, 0x05, 0xdc, 0, 0, 0x40, 0, 64, 6, 0, 0,
		10, 0, 0, 1, // src
		192, 0, 2, 9, // dst
	}
	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:2], 51515)
	binary.BigEndian.PutUint16(tcp[2:4], 443)
	tcp[13] = 0x18 // PSH,ACK
	return append(append(f, ip...), tcp...)
}

func rawHeader(proto, frameLen uint32, hdr []byte) []byte {
	return (&xdrBuf{}).u32(proto, frameLen, 4).opaque(hdr).b
}

// fullFlowSample is a compact flow sample with a raw Ethernet header and the
// switch, router and gateway extensions.
func fullFlowSample(w *xdrBuf) {
	s := &xdrBuf{}
	s.u32(7, 3)        // sequence, source id
	s.u32(512, 1e6, 0) // rate, pool, drops
	s.u32(3, 7)        // input, output: format 0, ifIndex 3 and 7
	s.u32(4)           // records
	s.item(sflowRawHeader, rawHeader(sflowHdrEthernet, 1514, ethTCP()))
	s.item(sflowExtSwitch, (&xdrBuf{}).u32(200, 0, 300, 0).b)
	s.item(sflowExtRouter, (&xdrBuf{}).u32(1).fixed([]byte{192, 0, 2, 254}).u32(24, 16).b)
	gw := (&xdrBuf{}).u32(1).fixed([]byte{198, 51, 100, 1})
	gw.u32(65000, 64512, 64513) // own AS, src AS, src peer AS
	gw.u32(1, 2, 3, 3356, 15169, 64999)
	gw.u32(0, 0) // communities, local pref
	s.item(sflowExtGateway, gw.b)
	w.item(sflowFlowSample, s.b)
}

func counterSample(w *xdrBuf) {
	w.item(2, (&xdrBuf{}).u32(1, 3, 1).item(1, make([]byte, 88)).b)
}

func TestSFlowDecodesFlowSampleWithExtensions(t *testing.T) {
	from := netip.MustParseAddr("198.51.100.7")
	p, err := DecodeSFlow(sflowDatagram(fullFlowSample), from)
	if err != nil {
		t.Fatal(err)
	}
	if p.ExporterIP != from {
		t.Errorf("exporter = %s, want the datagram source %s, not the agent address", p.ExporterIP, from)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(p.Records))
	}
	r := p.Records[0]
	checks := []struct {
		name      string
		got, want any
	}{
		{"src", r.SrcAddr, netip.MustParseAddr("10.0.0.1")},
		{"dst", r.DstAddr, netip.MustParseAddr("192.0.2.9")},
		{"proto", r.Protocol, uint8(6)},
		{"tos", r.TOS, uint8(0xB8)},
		{"sport", r.SrcPort, uint16(51515)},
		{"dport", r.DstPort, uint16(443)},
		{"flags", r.TCPFlags, uint8(0x18)},
		{"src mac", r.SrcMAC, uint64(0xaabbccddeeff)},
		{"dst mac", r.DstMAC, uint64(0x001122334455)},
		// The switch extension is the agent's own statement and wins over
		// the tag parsed from the header.
		{"src vlan", r.SrcVLAN, uint16(200)},
		{"dst vlan", r.DstVLAN, uint16(300)},
		{"next hop", r.NextHop, netip.MustParseAddr("192.0.2.254")},
		{"bgp next hop", r.BGPNextHop, netip.MustParseAddr("198.51.100.1")},
		{"src mask", r.SrcMask, uint8(24)},
		{"dst mask", r.DstMask, uint8(16)},
		{"src as", r.SrcAS, uint32(64512)},
		{"dst as", r.DstAS, uint32(64999)},
		{"in", r.InputIf, uint32(3)},
		{"out", r.OutputIf, uint32(7)},
		{"bytes", r.Bytes, uint64(1514 * 512)},
		{"packets", r.Packets, uint64(512)},
		{"rate", r.SamplingRate, uint32(512)},
		{"sampled", r.Sampled, true},
		{"type", r.Type, "sflow"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !r.Start.IsZero() || !r.End.IsZero() {
		t.Error("an sFlow sample has no duration; Start/End must stay zero")
	}
}

func TestSFlowHeaderVLANWithoutSwitchExtension(t *testing.T) {
	sample := func(w *xdrBuf) {
		s := (&xdrBuf{}).u32(1, 3, 1, 1, 0, 3, 7, 1)
		s.item(sflowRawHeader, rawHeader(sflowHdrEthernet, 1514, ethTCP()))
		w.item(sflowFlowSample, s.b)
	}
	p, err := DecodeSFlow(sflowDatagram(sample), netip.MustParseAddr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	r := p.Records[0]
	if r.SrcVLAN != 100 {
		t.Errorf("vlan = %d, want 100 from the 802.1Q tag", r.SrcVLAN)
	}
	if r.Sampled || r.SamplingRate != 1 || r.Bytes != 1514 || r.Packets != 1 {
		t.Errorf("rate 1 must be a plain count: sampled=%v rate=%d bytes=%d pkts=%d",
			r.Sampled, r.SamplingRate, r.Bytes, r.Packets)
	}
}

func TestSFlowExpandedSampleIPv6UDP(t *testing.T) {
	ip6 := make([]byte, 40)
	ip6[0], ip6[1] = 0x62, 0x80 // version 6, traffic class 0x28
	ip6[6] = 17                 // UDP
	copy(ip6[8:24], netip.MustParseAddr("2001:db8::1").AsSlice())
	copy(ip6[24:40], netip.MustParseAddr("2001:db8::2").AsSlice())
	udp := make([]byte, 8)
	binary.BigEndian.PutUint16(udp[0:2], 5353)
	binary.BigEndian.PutUint16(udp[2:4], 53)
	hdr := append(ip6, udp...)

	sample := func(w *xdrBuf) {
		s := &xdrBuf{}
		s.u32(9, 0, 5)   // sequence, source id type, source id index
		s.u32(100, 0, 0) // rate, pool, drops
		s.u32(0, 5)      // input: format 0, ifIndex 5
		s.u32(2, 3)      // output: format 2 — three interfaces, not one
		s.u32(1)
		s.item(sflowRawHeader, rawHeader(sflowHdrIPv6, 120, hdr))
		w.item(sflowExpandedFlowSample, s.b)
	}
	p, err := DecodeSFlow(sflowDatagram(sample), netip.MustParseAddr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(p.Records))
	}
	r := p.Records[0]
	if r.SrcAddr != netip.MustParseAddr("2001:db8::1") || r.DstAddr != netip.MustParseAddr("2001:db8::2") {
		t.Errorf("addresses = %s → %s", r.SrcAddr, r.DstAddr)
	}
	if r.Protocol != 17 || r.SrcPort != 5353 || r.DstPort != 53 {
		t.Errorf("proto/ports = %d %d→%d", r.Protocol, r.SrcPort, r.DstPort)
	}
	if r.TOS != 0x28 {
		t.Errorf("traffic class = %#x, want 0x28", r.TOS)
	}
	if r.InputIf != 5 {
		t.Errorf("input = %d, want 5", r.InputIf)
	}
	if r.OutputIf != 0 {
		t.Errorf("output = %d; a multiple-interface output names no single ifIndex", r.OutputIf)
	}
	if r.Bytes != 12000 || r.Packets != 100 {
		t.Errorf("bytes/packets = %d/%d, want 12000/100", r.Bytes, r.Packets)
	}
}

func TestSFlowICMPTypeAndCode(t *testing.T) {
	ip := []byte{0x45, 0, 0, 56, 0, 0, 0, 0, 64, 1, 0, 0, 10, 0, 0, 1, 10, 0, 0, 2}
	icmp := []byte{3, 1, 0, 0, 0, 0, 0, 0} // destination unreachable, host
	sample := func(w *xdrBuf) {
		s := (&xdrBuf{}).u32(1, 3, 1, 1, 0, sflowIfUnknown, 0x40000005, 1)
		s.item(sflowRawHeader, rawHeader(sflowHdrIPv4, 56, append(ip, icmp...)))
		w.item(sflowFlowSample, s.b)
	}
	p, err := DecodeSFlow(sflowDatagram(sample), netip.MustParseAddr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	r := p.Records[0]
	if r.ICMPType != 0x0301 {
		t.Errorf("icmp = %#04x, want 0x0301", r.ICMPType)
	}
	if r.SrcPort != 0 || r.DstPort != 0 {
		t.Errorf("ICMP has no ports, got %d→%d", r.SrcPort, r.DstPort)
	}
	// 0x3FFFFFFF is "unknown"; format 1 in the top bits is "discarded".
	if r.InputIf != 0 || r.OutputIf != 0 {
		t.Errorf("in/out = %d/%d, want 0/0", r.InputIf, r.OutputIf)
	}
}

func TestSFlowIPDataRecordIsAFallback(t *testing.T) {
	sample := func(w *xdrBuf) {
		s := (&xdrBuf{}).u32(1, 3, 10, 1, 0, 3, 7, 1)
		d := (&xdrBuf{}).u32(1500, 6).fixed([]byte{10, 1, 1, 1}).fixed([]byte{10, 2, 2, 2})
		d.u32(1234, 22, 0x02, 0x10)
		s.item(sflowIPv4Data, d.b)
		w.item(sflowFlowSample, s.b)
	}
	p, err := DecodeSFlow(sflowDatagram(sample), netip.MustParseAddr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	r := p.Records[0]
	if r.SrcAddr != netip.MustParseAddr("10.1.1.1") || r.DstPort != 22 || r.TCPFlags != 0x02 {
		t.Errorf("ip data record not used: %+v", r)
	}
	if r.Bytes != 15000 {
		t.Errorf("bytes = %d, want 1500×10", r.Bytes)
	}
}

func TestSFlowSkipsCounterSamplesAndNonIP(t *testing.T) {
	arp := func(w *xdrBuf) {
		frame := make([]byte, 42)
		frame[12], frame[13] = 0x08, 0x06 // ARP
		s := (&xdrBuf{}).u32(1, 3, 1, 1, 0, 3, 7, 1)
		s.item(sflowRawHeader, rawHeader(sflowHdrEthernet, 60, frame))
		w.item(sflowFlowSample, s.b)
	}
	vendor := func(w *xdrBuf) { w.item(9<<12|1, make([]byte, 32)) }
	p, err := DecodeSFlow(sflowDatagram(counterSample, arp, vendor, fullFlowSample), netip.MustParseAddr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 1 {
		t.Fatalf("records = %d, want only the IP flow sample", len(p.Records))
	}
}

func TestSFlowRejectsTruncationAndGarbage(t *testing.T) {
	from := netip.MustParseAddr("192.0.2.1")
	full := sflowDatagram(counterSample, fullFlowSample)
	if _, err := DecodeSFlow(full, from); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// Every truncation cuts the last sample's framing or the header, so
	// every one must fail — and none may panic.
	for n := 0; n < len(full); n++ {
		if _, err := DecodeSFlow(full[:n], from); err == nil {
			t.Errorf("truncated to %d of %d bytes: want error", n, len(full))
		}
	}

	bad := map[string][]byte{
		"netflow v5":     {0, 5, 0, 1},
		"version 4":      (&xdrBuf{}).u32(4, 1, 0, 0, 0, 0, 0).b,
		"addr type 7":    (&xdrBuf{}).u32(5, 7, 0, 0, 0, 0, 0).b,
		"sample count":   (&xdrBuf{}).u32(5, 1, 0, 0, 0, 0, 1<<20).b,
		"sample length":  append((&xdrBuf{}).u32(5, 1, 0, 0, 0, 0, 1, 1).b, 0xff, 0xff, 0xff, 0xff),
		"no samples yet": (&xdrBuf{}).u32(5, 1, 0, 0, 0, 0, 2).b,
	}
	for name, b := range bad {
		if _, err := DecodeSFlow(b, from); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSFlowMalformedSampleDropsOnlyItself(t *testing.T) {
	// A flow sample whose record count is absurd, framed correctly: it is
	// skipped, and the good sample after it still decodes.
	forged := func(w *xdrBuf) { w.item(sflowFlowSample, (&xdrBuf{}).u32(1, 3, 1, 1, 0, 3, 7, 1<<30).b) }
	// A gateway record claiming a huge AS path.
	gwBomb := func(w *xdrBuf) {
		s := (&xdrBuf{}).u32(1, 3, 1, 1, 0, 3, 7, 2)
		s.item(sflowRawHeader, rawHeader(sflowHdrEthernet, 1514, ethTCP()))
		gw := (&xdrBuf{}).u32(1).fixed([]byte{198, 51, 100, 1}).u32(65000, 1, 2, 1, 2, 0xFFFFFFFF)
		s.item(sflowExtGateway, gw.b)
		w.item(sflowFlowSample, s.b)
	}
	p, err := DecodeSFlow(sflowDatagram(forged, gwBomb, fullFlowSample), netip.MustParseAddr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 2 {
		t.Fatalf("records = %d, want 2 (the forged sample dropped alone)", len(p.Records))
	}
	if p.Records[0].DstAS != 0 {
		t.Errorf("a forged AS path must not set DstAS, got %d", p.Records[0].DstAS)
	}
}

func TestIsSFlow(t *testing.T) {
	sf := sflowDatagram(fullFlowSample)
	if !IsSFlow(sf) {
		t.Error("sFlow datagram not recognised")
	}
	if v, ok := Version(sf); !ok || v != 0 {
		t.Errorf("Version(sFlow) = %d,%v; the collector relies on it reading 0", v, ok)
	}
	v5 := make([]byte, netflowV5HeaderLen)
	v5[1], v5[3] = 5, 1
	if IsSFlow(v5) {
		t.Error("NetFlow v5 header mistaken for sFlow")
	}
	if IsSFlow([]byte{0, 0, 0}) {
		t.Error("short datagram mistaken for sFlow")
	}
}

func FuzzDecodeSFlow(f *testing.F) {
	f.Add(sflowDatagram(counterSample, fullFlowSample))
	f.Add(sflowDatagram(fullFlowSample))
	from := netip.MustParseAddr("192.0.2.1")
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := DecodeSFlow(b, from)
		if err != nil {
			return
		}
		for _, r := range p.Records {
			if !r.SrcAddr.IsValid() || !r.DstAddr.IsValid() {
				t.Fatalf("record without addresses: %+v", r)
			}
			if r.SamplingRate == 0 {
				t.Fatalf("zero sampling rate: %+v", r)
			}
		}
	})
}
