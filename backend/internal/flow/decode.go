// Package flow decodes exported flow records and delivers them two ways: as
// the few bounded-cardinality series NetInv keeps in VictoriaMetrics
// (ADR-020), and — when a ClickHouse store is configured — one row per flow
// (ADR-026).
//
// The two paths are deliberately independent. A flow record is a wide event
// keyed by source, destination, port and protocol, and one busy link produces
// more distinct keys in an hour than the whole device fleet produces series in
// a year, so the aggregator cuts to top-N before anything reaches the metric
// store; the raw rows go to a columnar store built for exactly that width.
// Losing the raw store never touches the aggregates.
package flow

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
)

// Record is one decoded flow, normalised across export formats.
type Record struct {
	SrcAddr  netip.Addr
	DstAddr  netip.Addr
	SrcPort  uint16
	DstPort  uint16
	Protocol uint8
	// TOS is the IP type-of-service byte; its top six bits are the DSCP.
	TOS uint8
	// SrcAS and DstAS are the exporter's BGP view of each end. Zero means the
	// exporter did not know or did not say — most edge gateways never fill it.
	SrcAS uint32
	DstAS uint32
	// SrcMask and DstMask are the prefix lengths of the exporter's routes to
	// each end. Zero means not reported — or a default route, which says as
	// little — and the subnet dimension then falls back to a fixed length.
	SrcMask uint8
	DstMask uint8
	// TCPFlags is the OR of every TCP flag seen across the flow's packets;
	// meaningless unless Protocol is 6.
	TCPFlags uint8
	// Bytes and Packets are already scaled by the sampling rate where the
	// exporter reported one, so callers do not have to remember to.
	Bytes   uint64
	Packets uint64
	// InputIf and OutputIf are the exporter's SNMP ifIndex values, which is
	// what lets a flow be attributed to an interface NetInv already knows.
	InputIf  uint32
	OutputIf uint32
	// Sampled is true when the numbers are an extrapolation rather than a
	// count. It travels with the record so the UI can say so.
	Sampled bool
	// SamplingRate is the 1-in-N the counters were scaled by; 1 when the
	// record is a full count.
	SamplingRate uint32

	// The fields below feed raw storage (ADR-026) only; the aggregator does
	// not read them. Each is the zero value when the export did not carry it,
	// which is the common case — an exporter sends what its template names.

	// Start and End are the flow's first and last packet in wall-clock time,
	// converted from the exporter's uptime-relative clock where that is how
	// the format carries them. Zero when not exported.
	Start, End time.Time
	// SrcMAC and DstMAC as 48-bit integers, the form ClickHouse stores.
	SrcMAC, DstMAC uint64
	SrcVLAN        uint16
	DstVLAN        uint16
	// NextHop is the IP next hop; BGPNextHop the BGP one.
	NextHop    netip.Addr
	BGPNextHop netip.Addr
	// Direction is 1 for ingress and 2 for egress as the exporter saw the
	// flow, 0 when it did not say. (The wire value is 0/1; shifting it keeps
	// "unknown" distinguishable from "ingress".)
	Direction uint8
	// ICMPType is type<<8 | code for ICMP and ICMPv6 flows.
	ICMPType uint16
	// AppID is the exporter's application classification as
	// "engine:selector" (NBAR, App-ID, FortiOS application control);
	// AppName its name when the exporter sends one inline.
	AppID   string
	AppName string
	// Post-NAT addresses and ports, from firewalls that export translation.
	NATSrcAddr netip.Addr
	NATDstAddr netip.Addr
	NATSrcPort uint16
	NATDstPort uint16
	// FWEvent is the firewall event code (IPFIX firewallEvent: 1 created,
	// 2 deleted, 3 denied, 4 alert, 5 update).
	FWEvent uint8
	// User is the authenticated user the firewall attributed the flow to.
	User string
	// Type names the export format: netflow5, netflow9, ipfix or sflow.
	Type string
}

// Packet is a decoded export packet.
//
// It carries no timestamp. A v5 record's times are relative to the exporter's
// uptime, and the aggregator stamps each interval with its own drain time
// anyway (see collector.go), so decoding the export's clock would produce a
// field that exists only to be ignored — and to suggest to the next reader
// that flow timestamps come from the device, which they do not.
type Packet struct {
	// ExporterIP is the source address of the datagram, not anything inside
	// it: NetFlow v5 carries no exporter identity, and a spoofable field would
	// be a poor key for attributing traffic to a device anyway.
	ExporterIP netip.Addr
	Records    []Record
}

const (
	netflowV5HeaderLen = 24
	netflowV5RecordLen = 48
	// A v5 packet holds at most 30 records by specification. Anything larger
	// is malformed or hostile, and believing the count field would let a
	// 24-byte datagram claim 65535 records.
	netflowV5MaxRecords = 30
)

// ErrShortPacket means the datagram ended before the structure it declared.
type ErrShortPacket struct{ Want, Got int }

func (e ErrShortPacket) Error() string {
	return fmt.Sprintf("flow: truncated packet: need %d bytes, have %d", e.Want, e.Got)
}

// Version reads the export version without decoding the rest, so a listener
// can route a datagram to the right decoder.
func Version(b []byte) (uint16, bool) {
	if len(b) < 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(b[:2]), true
}

// DecodeNetFlowV5 parses a NetFlow v5 export packet.
//
// v5 is fixed-layout with no templates, which makes it the one format a
// collector can decode without carrying per-exporter state. That is why it is
// first: v9 and IPFIX cannot decode a packet at all until the exporter has
// sent a matching template, so they need state, expiry and a story for what to
// do with data that arrives before its template.
func DecodeNetFlowV5(b []byte, from netip.Addr) (*Packet, error) {
	if len(b) < netflowV5HeaderLen {
		return nil, ErrShortPacket{Want: netflowV5HeaderLen, Got: len(b)}
	}
	version := binary.BigEndian.Uint16(b[0:2])
	if version != 5 {
		return nil, fmt.Errorf("flow: not a NetFlow v5 packet (version %d)", version)
	}
	count := int(binary.BigEndian.Uint16(b[2:4]))
	if count > netflowV5MaxRecords {
		return nil, fmt.Errorf("flow: v5 packet claims %d records, max is %d",
			count, netflowV5MaxRecords)
	}
	need := netflowV5HeaderLen + count*netflowV5RecordLen
	if len(b) < need {
		return nil, ErrShortPacket{Want: need, Got: len(b)}
	}

	// Bits 14-15 select the sampling mode; the low 14 bits are the interval.
	// Mode 0 means no sampling, and an interval of 0 or 1 means one-for-one —
	// treating either as a multiplier would zero or double every byte count.
	clock := exportClock{
		uptimeMs: binary.BigEndian.Uint32(b[4:8]),
		unix:     time.Unix(int64(binary.BigEndian.Uint32(b[8:12])), int64(binary.BigEndian.Uint32(b[12:16]))),
	}
	samplingInterval := binary.BigEndian.Uint16(b[22:24]) & 0x3FFF
	samplingMode := binary.BigEndian.Uint16(b[22:24]) >> 14
	scale := uint64(1)
	rate := uint32(1)
	sampled := false
	if samplingMode != 0 && samplingInterval > 1 {
		scale = uint64(samplingInterval)
		rate = uint32(samplingInterval)
		sampled = true
	}

	p := &Packet{ExporterIP: from, Records: make([]Record, 0, count)}

	for i := 0; i < count; i++ {
		// Convert to a fixed-size array rather than reslicing. The length
		// check above already guarantees the bytes are present, but only
		// across the whole loop; an array makes every field read provably in
		// range at the point it happens — to a reader and to gosec alike —
		// instead of resting on an invariant established forty lines earlier.
		// It costs one 48-byte copy per record.
		off := netflowV5HeaderLen + i*netflowV5RecordLen
		r := [netflowV5RecordLen]byte(b[off : off+netflowV5RecordLen])
		src, okS := netip.AddrFromSlice(r[0:4])
		dst, okD := netip.AddrFromSlice(r[4:8])
		if !okS || !okD {
			continue
		}
		hop, _ := netip.AddrFromSlice(r[8:12])
		if hop.IsUnspecified() {
			hop = netip.Addr{}
		}
		p.Records = append(p.Records, Record{
			SrcAddr:      src,
			DstAddr:      dst,
			InputIf:      uint32(binary.BigEndian.Uint16(r[12:14])),
			OutputIf:     uint32(binary.BigEndian.Uint16(r[14:16])),
			Packets:      uint64(binary.BigEndian.Uint32(r[16:20])) * scale,
			Bytes:        uint64(binary.BigEndian.Uint32(r[20:24])) * scale,
			SrcPort:      binary.BigEndian.Uint16(r[32:34]),
			DstPort:      binary.BigEndian.Uint16(r[34:36]),
			TCPFlags:     r[37],
			Protocol:     r[38],
			TOS:          r[39],
			SrcAS:        uint32(binary.BigEndian.Uint16(r[40:42])),
			DstAS:        uint32(binary.BigEndian.Uint16(r[42:44])),
			SrcMask:      r[44],
			DstMask:      r[45],
			Sampled:      sampled,
			SamplingRate: rate,
			Start:        clock.at(binary.BigEndian.Uint32(r[24:28])),
			End:          clock.at(binary.BigEndian.Uint32(r[28:32])),
			NextHop:      hop,
			Type:         "netflow5",
		})
	}
	return p, nil
}

// exportClock converts the uptime-relative timestamps of NetFlow v5 and v9
// (milliseconds since the exporter booted) to wall-clock time, using the
// export packet's own pairing of uptime and Unix time.
type exportClock struct {
	uptimeMs uint32
	unix     time.Time
}

// at returns the wall-clock time of an uptime reading, or zero for a zero
// reading — "not exported" — and for a clock the packet never set.
func (c exportClock) at(uptimeMs uint32) time.Time {
	if uptimeMs == 0 || c.unix.IsZero() || c.unix.Unix() == 0 {
		return time.Time{}
	}
	// Subtract in uint32 and read the result as signed. That is right across
	// the 49.7-day uptime wrap, and for a last packet stamped a moment after
	// the header's uptime was read (a small negative age), where plain
	// unsigned arithmetic would put it 49 days in the past.
	age := int32(c.uptimeMs - uptimeMs) //nolint:gosec // wrap is the intent
	return c.unix.Add(-time.Duration(age) * time.Millisecond).UTC()
}
