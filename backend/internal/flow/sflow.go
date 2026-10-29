package flow

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// sFlow v5 decoding (sflow.org, sflow_version_5.txt).
//
// sFlow is not a flow export at all, and treating it as one is the mistake to
// avoid. A NetFlow exporter tracks connections and reports each one once; an
// sFlow agent copies the first bytes of one packet in N and sends that,
// together with N. There is no duration, no packet count beyond "this sample
// stands for N", and no TCP-flag accumulation. Each sample therefore becomes
// one Record whose counters are the sampled frame scaled by the rate — the
// same extrapolation a sampled NetFlow exporter does internally, done here.
//
// The format is XDR: every field is 4-byte aligned and every variable part is
// length-prefixed. Those lengths come off an unauthenticated UDP socket, so
// every one is checked against what is actually left, sample and record counts
// are capped, and each loop step consumes at least its own header — a hostile
// datagram can be rejected, never made to loop or read out of range.
const (
	sflowVersion = 5
	// A datagram is at most 64 KB; the smallest useful flow sample is far
	// larger than 128 bytes, so 512 is generous for anything real while
	// bounding the work a forged count can ask for.
	sflowMaxSamples = 512
	// Agents send a handful of records per sample (header, switch, router,
	// gateway). 64 leaves room for vendor extensions without letting one
	// sample claim millions.
	sflowMaxRecords = 64

	// Sample formats (enterprise 0).
	sflowFlowSample         = 1
	sflowExpandedFlowSample = 3

	// Flow record formats (enterprise 0).
	sflowRawHeader      = 1
	sflowEthernetFrame  = 2
	sflowIPv4Data       = 3
	sflowIPv6Data       = 4
	sflowExtSwitch      = 1001
	sflowExtRouter      = 1002
	sflowExtGateway     = 1003
	sflowHdrEthernet    = 1  // header_protocol ETHERNET-ISO88023
	sflowHdrIPv4        = 11 // header_protocol IPv4
	sflowHdrIPv6        = 12 // header_protocol IPv6
	sflowIfUnknown      = 0x3FFFFFFF
	sflowAddrIPv4       = 1
	sflowAddrIPv6       = 2
	sflowMaxASPathTotal = 256
)

// IsSFlow reports whether a datagram starts with an sFlow v5 header. sFlow's
// version is a 32-bit word, so its first two bytes are zero — which is why
// Version reads it as 0 and a collector has to ask this instead. A NetFlow
// v5 header reads 0x0005xxxx here and is never mistaken for it.
func IsSFlow(b []byte) bool {
	return len(b) >= 4 && binary.BigEndian.Uint32(b[:4]) == sflowVersion
}

// xdrReader walks XDR. Any read past the end sets bad and returns zeros, so a
// decoder can read a whole structure and check once, instead of after every
// field — and a truncated structure can never yield a half-read value that
// looks valid, because the caller discards it on bad.
type xdrReader struct {
	b   []byte
	off int
	bad bool
}

func (x *xdrReader) u32() uint32 {
	if x.bad || x.off+4 > len(x.b) {
		x.bad = true
		return 0
	}
	v := binary.BigEndian.Uint32(x.b[x.off : x.off+4])
	x.off += 4
	return v
}

// take returns the next n bytes and advances past them plus XDR's padding to
// a 4-byte boundary.
func (x *xdrReader) take(n uint32) []byte {
	// Both sides widened to int64, which holds any uint32 and any slice
	// length, so the comparison itself cannot overflow.
	if x.bad || int64(n) > int64(len(x.b)-x.off) {
		x.bad = true
		return nil
	}
	end := x.off + int(n)
	out := x.b[x.off:end]
	pad := (4 - int(n)%4) % 4
	if end+pad > len(x.b) {
		// The value is all there but its padding is not. Accept the value
		// — some agents omit trailing padding on the last field — and stop
		// at the end.
		x.off = len(x.b)
		return out
	}
	x.off = end + pad
	return out
}

func (x *xdrReader) skip(n int) {
	if x.bad || n > len(x.b)-x.off {
		x.bad = true
		return
	}
	x.off += n
}

// addr reads an sFlow address: a type word, then 4 or 16 bytes. Type 0
// ("unknown") carries no bytes and yields no address.
func (x *xdrReader) addr() netip.Addr {
	switch x.u32() {
	case sflowAddrIPv4:
		return addrOrZero(x.take(4))
	case sflowAddrIPv6:
		return addrOrZero(x.take(16))
	case 0:
		return netip.Addr{}
	default:
		// An unknown address type has an unknown width, so nothing after it
		// can be located.
		x.bad = true
		return netip.Addr{}
	}
}

// DecodeSFlow parses an sFlow v5 datagram. Flow samples become records;
// counter samples and formats this decoder does not know are skipped by their
// declared length, which is what lets an agent send vendor extensions without
// breaking the samples we do read.
//
// ExporterIP is the datagram's source, not the agent address inside it, for
// the same reason as NetFlow v5: the field is spoofable and the source is
// what flow attribution (doc 34 §3.1) is keyed on everywhere else.
func DecodeSFlow(b []byte, from netip.Addr) (*Packet, error) {
	x := &xdrReader{b: b}
	if v := x.u32(); x.bad || v != sflowVersion {
		if x.bad {
			return nil, ErrShortPacket{Want: 4, Got: len(b)}
		}
		return nil, fmt.Errorf("flow: not an sFlow v5 datagram (version %d)", v)
	}
	x.addr()  // agent address: deliberately unused, see above
	x.skip(4) // sub-agent id
	x.skip(4) // sequence number
	x.skip(4) // uptime
	n := x.u32()
	if x.bad {
		return nil, fmt.Errorf("flow: truncated sFlow header (%d bytes)", len(b))
	}
	if n > sflowMaxSamples {
		return nil, fmt.Errorf("flow: sFlow datagram claims %d samples, max is %d", n, sflowMaxSamples)
	}

	p := &Packet{ExporterIP: from}
	for i := uint32(0); i < n; i++ {
		format := x.u32()
		body := x.take(x.u32())
		if x.bad {
			// The framing itself is broken: nothing after this point can be
			// located, and samples already read came from a datagram that
			// lies about its own structure.
			return nil, fmt.Errorf("flow: sFlow sample %d of %d does not fit the datagram", i+1, n)
		}
		if format>>12 != 0 {
			continue // enterprise-specific sample: no standard meaning
		}
		switch format & 0xFFF {
		case sflowFlowSample:
			if r, ok := decodeSFlowSample(body, false); ok {
				p.Records = append(p.Records, r)
			}
		case sflowExpandedFlowSample:
			if r, ok := decodeSFlowSample(body, true); ok {
				p.Records = append(p.Records, r)
			}
		default:
			// Counter samples (2, 4) describe interfaces, not traffic; the
			// SNMP poller already reads the same counters.
		}
	}
	return p, nil
}

// sflowIf turns an sFlow interface word into an ifIndex. Only format 0 is a
// single interface; 1 is "dropped" (with a reason code in the value) and 2 is
// "several interfaces" (a count), and neither identifies one port — reading
// either as an ifIndex would attribute traffic to whatever port has that
// number. 0x3FFFFFFF is the explicit "unknown".
func sflowIf(format, value uint32) uint32 {
	if format != 0 || value == sflowIfUnknown {
		return 0
	}
	return value
}

// decodeSFlowSample reads one flow sample (compact or expanded form). A
// malformed sample is dropped on its own: its outer length already framed it,
// so the samples after it are still readable.
func decodeSFlowSample(body []byte, expanded bool) (Record, bool) {
	x := &xdrReader{b: body}
	r := Record{Type: "sflow"}
	x.skip(4) // sequence number
	if expanded {
		x.skip(8) // source id type, source id index
	} else {
		x.skip(4) // source id
	}
	rate := x.u32()
	x.skip(4) // sample pool
	x.skip(4) // drops
	if expanded {
		inF, inV := x.u32(), x.u32()
		outF, outV := x.u32(), x.u32()
		r.InputIf, r.OutputIf = sflowIf(inF, inV), sflowIf(outF, outV)
	} else {
		in, out := x.u32(), x.u32()
		r.InputIf = sflowIf(in>>30, in&0x3FFFFFFF)
		r.OutputIf = sflowIf(out>>30, out&0x3FFFFFFF)
	}
	n := x.u32()
	if x.bad || n > sflowMaxRecords {
		return Record{}, false
	}

	// frameLen comes from the raw header if there is one, else from the IP
	// data record — the length of what was sampled, before scaling.
	var frameLen uint32
	var haveHeader bool
	for i := uint32(0); i < n; i++ {
		format := x.u32()
		rec := x.take(x.u32())
		if x.bad {
			return Record{}, false
		}
		if format>>12 != 0 {
			continue
		}
		switch format & 0xFFF {
		case sflowRawHeader:
			if l, ok := sflowReadRawHeader(rec, &r); ok {
				frameLen, haveHeader = l, true
			}
		case sflowEthernetFrame:
			sflowReadEthernetData(rec, &r)
		case sflowIPv4Data, sflowIPv6Data:
			// A fallback for agents that send decoded IP fields instead of
			// the header; the header, when present, is the better source.
			if l, ok := sflowReadIPData(rec, &r, format&0xFFF == sflowIPv6Data, haveHeader); ok && !haveHeader {
				frameLen = l
			}
		case sflowExtSwitch:
			sflowReadExtSwitch(rec, &r)
		case sflowExtRouter:
			sflowReadExtRouter(rec, &r)
		case sflowExtGateway:
			sflowReadExtGateway(rec, &r)
		}
	}

	// Same rule as v9: a sample with no addresses is not traffic we can say
	// anything about (a non-IP frame, or a header too short to reach them).
	if !r.SrcAddr.IsValid() || !r.DstAddr.IsValid() {
		return Record{}, false
	}
	if rate == 0 {
		rate = 1 // a zero rate is meaningless; count the sample as itself
	}
	r.SamplingRate = rate
	r.Sampled = rate > 1
	r.Packets = uint64(rate)
	r.Bytes = uint64(frameLen) * uint64(rate)
	return r, true
}

// sflowReadRawHeader decodes a sampled packet header record and returns the
// original frame length.
func sflowReadRawHeader(rec []byte, r *Record) (uint32, bool) {
	x := &xdrReader{b: rec}
	proto := x.u32()
	frameLen := x.u32()
	x.skip(4) // stripped: bytes removed before the header was taken
	hdr := x.take(x.u32())
	if x.bad {
		return 0, false
	}
	switch proto {
	case sflowHdrEthernet:
		parseEthernetHeader(hdr, r)
	case sflowHdrIPv4:
		parseIPv4Header(hdr, r)
	case sflowHdrIPv6:
		parseIPv6Header(hdr, r)
	}
	return frameLen, true
}

// parseEthernetHeader reads MACs and VLAN tags, then the IP header behind
// them. Up to two tags are walked, which covers QinQ; the outer one is the
// VLAN the port sees and is the one recorded.
func parseEthernetHeader(h []byte, r *Record) {
	if len(h) < 14 {
		return
	}
	r.DstMAC = beUint(h[0:6])
	r.SrcMAC = beUint(h[6:12])
	et := binary.BigEndian.Uint16(h[12:14])
	off := 14
	for tags := 0; tags < 2 && (et == 0x8100 || et == 0x88A8 || et == 0x9100); tags++ {
		if off+4 > len(h) {
			return
		}
		if tags == 0 {
			r.SrcVLAN = binary.BigEndian.Uint16(h[off:off+2]) & 0x0FFF
		}
		et = binary.BigEndian.Uint16(h[off+2 : off+4])
		off += 4
	}
	switch et {
	case 0x0800:
		parseIPv4Header(h[off:], r)
	case 0x86DD:
		parseIPv6Header(h[off:], r)
	}
}

func parseIPv4Header(h []byte, r *Record) {
	if len(h) < 20 || h[0]>>4 != 4 {
		return
	}
	ihl := int(h[0]&0x0F) * 4
	if ihl < 20 {
		return
	}
	r.TOS = h[1]
	r.Protocol = h[9]
	r.SrcAddr = netip.AddrFrom4([4]byte(h[12:16]))
	r.DstAddr = netip.AddrFrom4([4]byte(h[16:20]))
	// A non-first fragment carries no transport header: reading "ports"
	// from it would report payload bytes as port numbers.
	if binary.BigEndian.Uint16(h[6:8])&0x1FFF != 0 || ihl > len(h) {
		return
	}
	parseL4(h[ihl:], r)
}

func parseIPv6Header(h []byte, r *Record) {
	if len(h) < 40 || h[0]>>4 != 6 {
		return
	}
	r.TOS = h[0]<<4 | h[1]>>4 // traffic class, which carries the DSCP like TOS
	r.SrcAddr = netip.AddrFrom16([16]byte(h[8:24])).Unmap()
	r.DstAddr = netip.AddrFrom16([16]byte(h[24:40])).Unmap()
	next, off := h[6], 40
	// Walk the common extension headers to reach the transport header. The
	// bound keeps a forged chain from costing more than a few steps.
	for hops := 0; hops < 4; hops++ {
		switch next {
		case 0, 43, 60: // hop-by-hop, routing, destination options
			if off+2 > len(h) {
				r.Protocol = next
				return
			}
			next, off = h[off], off+(int(h[off+1])+1)*8
		case 44: // fragment
			if off+8 > len(h) {
				r.Protocol = next
				return
			}
			fragOff := binary.BigEndian.Uint16(h[off+2:off+4]) >> 3
			next, off = h[off], off+8
			if fragOff != 0 {
				r.Protocol = next
				return
			}
		default:
			r.Protocol = next
			if off <= len(h) {
				parseL4(h[off:], r)
			}
			return
		}
	}
	r.Protocol = next
}

// parseL4 reads ports and TCP flags, or ICMP type and code.
func parseL4(h []byte, r *Record) {
	switch r.Protocol {
	case 6: // TCP
		if len(h) >= 4 {
			r.SrcPort = binary.BigEndian.Uint16(h[0:2])
			r.DstPort = binary.BigEndian.Uint16(h[2:4])
		}
		if len(h) >= 14 {
			r.TCPFlags = h[13]
		}
	case 17, 132: // UDP, SCTP
		if len(h) >= 4 {
			r.SrcPort = binary.BigEndian.Uint16(h[0:2])
			r.DstPort = binary.BigEndian.Uint16(h[2:4])
		}
	case 1, 58: // ICMP, ICMPv6
		if len(h) >= 2 {
			r.ICMPType = uint16(h[0])<<8 | uint16(h[1])
		}
	}
}

// sflowReadEthernetData reads the "ethernet frame data" record. MACs are
// taken only when the raw header did not already supply them.
func sflowReadEthernetData(rec []byte, r *Record) {
	x := &xdrReader{b: rec}
	x.skip(4) // length
	src := x.take(6)
	dst := x.take(6)
	if x.bad {
		return
	}
	if r.SrcMAC == 0 {
		r.SrcMAC = beUint(src)
	}
	if r.DstMAC == 0 {
		r.DstMAC = beUint(dst)
	}
}

// sflowReadIPData reads the decoded IPv4/IPv6 data record. Its fields are used
// only when no raw header was decoded; its length is returned either way.
func sflowReadIPData(rec []byte, r *Record, v6, haveHeader bool) (uint32, bool) {
	x := &xdrReader{b: rec}
	length := x.u32()
	proto := x.u32()
	var src, dst []byte
	if v6 {
		src, dst = x.take(16), x.take(16)
	} else {
		src, dst = x.take(4), x.take(4)
	}
	sport, dport := x.u32(), x.u32()
	flags, tos := x.u32(), x.u32()
	if x.bad {
		return 0, false
	}
	if !haveHeader && !r.SrcAddr.IsValid() {
		r.Protocol = narrow8(uint64(proto))
		r.SrcAddr = addrOrZero(src)
		r.DstAddr = addrOrZero(dst)
		r.SrcPort = narrow16(uint64(sport))
		r.DstPort = narrow16(uint64(dport))
		r.TCPFlags = narrow8(uint64(flags))
		r.TOS = narrow8(uint64(tos))
	}
	return length, true
}

// sflowReadExtSwitch reads the 802.1Q VLANs the switch saw on ingress and
// egress. It is the agent's own statement and wins over a tag parsed from the
// header, which an access port will not even carry.
func sflowReadExtSwitch(rec []byte, r *Record) {
	x := &xdrReader{b: rec}
	srcVLAN := x.u32()
	x.skip(4) // src priority
	dstVLAN := x.u32()
	if x.bad {
		return
	}
	if v := narrow16(uint64(srcVLAN)) & 0x0FFF; v != 0 {
		r.SrcVLAN = v
	}
	if v := narrow16(uint64(dstVLAN)) & 0x0FFF; v != 0 {
		r.DstVLAN = v
	}
}

// sflowReadExtRouter reads the IP next hop and the prefix lengths of the
// routes to each end.
func sflowReadExtRouter(rec []byte, r *Record) {
	x := &xdrReader{b: rec}
	hop := x.addr()
	srcMask, dstMask := x.u32(), x.u32()
	if x.bad {
		return
	}
	r.NextHop = hop
	r.SrcMask = narrow8(uint64(srcMask))
	r.DstMask = narrow8(uint64(dstMask))
}

// sflowReadExtGateway reads the BGP view: next hop, source AS, and the AS
// path to the destination, whose last AS is the destination's origin.
func sflowReadExtGateway(rec []byte, r *Record) {
	x := &xdrReader{b: rec}
	hop := x.addr()
	ownAS := x.u32()
	srcAS := x.u32()
	x.skip(4) // src peer AS
	segments := x.u32()
	if x.bad || segments > sflowMaxASPathTotal {
		return
	}
	var dstAS uint32
	total := uint32(0)
	for i := uint32(0); i < segments; i++ {
		x.skip(4) // segment type: set or sequence
		count := x.u32()
		// Checked before adding, so a forged count cannot wrap the total.
		if x.bad || count > sflowMaxASPathTotal-total {
			return
		}
		total += count
		for j := uint32(0); j < count; j++ {
			dstAS = x.u32()
		}
	}
	if x.bad {
		return
	}
	// An empty path means the destination is inside the router's own AS.
	if segments == 0 {
		dstAS = ownAS
	}
	r.BGPNextHop = hop
	r.SrcAS = srcAS
	r.DstAS = dstAS
}
