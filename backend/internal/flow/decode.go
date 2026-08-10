// Package flow decodes exported flow records and reduces them to the few
// bounded-cardinality series NetInv keeps (ADR-020).
//
// Nothing here stores a flow. A flow record is a wide event keyed by source,
// destination, port and protocol, and one busy link produces more distinct
// keys in an hour than the whole device fleet produces series in a year. The
// aggregator cuts to top-N before anything is written, which is why this
// package can exist alongside VictoriaMetrics rather than needing a columnar
// store of its own.
package flow

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// Record is one decoded flow, normalised across export formats.
type Record struct {
	SrcAddr  netip.Addr
	DstAddr  netip.Addr
	SrcPort  uint16
	DstPort  uint16
	Protocol uint8
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
	samplingInterval := binary.BigEndian.Uint16(b[22:24]) & 0x3FFF
	samplingMode := binary.BigEndian.Uint16(b[22:24]) >> 14
	scale := uint64(1)
	sampled := false
	if samplingMode != 0 && samplingInterval > 1 {
		scale = uint64(samplingInterval)
		sampled = true
	}

	p := &Packet{ExporterIP: from, Records: make([]Record, 0, count)}

	for i := 0; i < count; i++ {
		r := b[netflowV5HeaderLen+i*netflowV5RecordLen:]
		src, okS := netip.AddrFromSlice(r[0:4])
		dst, okD := netip.AddrFromSlice(r[4:8])
		if !okS || !okD {
			continue
		}
		p.Records = append(p.Records, Record{
			SrcAddr:  src,
			DstAddr:  dst,
			InputIf:  uint32(binary.BigEndian.Uint16(r[12:14])),
			OutputIf: uint32(binary.BigEndian.Uint16(r[14:16])),
			Packets:  uint64(binary.BigEndian.Uint32(r[16:20])) * scale,
			Bytes:    uint64(binary.BigEndian.Uint32(r[20:24])) * scale,
			SrcPort:  binary.BigEndian.Uint16(r[32:34]),
			DstPort:  binary.BigEndian.Uint16(r[34:36]),
			Protocol: r[38],
			Sampled:  sampled,
		})
	}
	return p, nil
}
