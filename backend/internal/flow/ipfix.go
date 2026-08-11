package flow

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
)

// IPFIX (RFC 7011) decoding.
//
// IPFIX is v9's template model with the corners filed off, so it reuses the
// cache, the record walker and the field mapping. What it does not share is
// worth listing, because each item is a place where treating it as "v9 with a
// different version number" reads the wrong bytes without failing:
//
//   - The header is 16 bytes, not 20, and carries the message's **total
//     length** where v9 carries a **record count**. A decoder that trusted the
//     v9 field would walk past the end or stop early.
//   - Template sets are ID 2 and options templates ID 3; in v9 they are 0 and
//     1. The reserved range is otherwise the same.
//   - An options template declares a scope field *count*. v9 declares scope and
//     option *byte lengths*. Same idea, different unit, and both are 16-bit
//     fields sitting in the same place.
//   - Fields may be enterprise-specific: the high bit of the element ID means
//     a 4-byte enterprise number follows in the template. Miss it and every
//     subsequent field in that template is offset by four bytes.
//   - A field may be variable-length, with its width in the record rather than
//     the template (handled by eachRecord).
const (
	ipfixHeaderLen      = 16
	ipfixSetTemplate    = 2
	ipfixSetOptionsTmpl = 3
	ipfixSetMinData     = 256
	// enterpriseBit marks an element ID as vendor-specific.
	enterpriseBit = 0x8000
)

// DecodeIPFIX parses an IPFIX message against the template cache, learning any
// templates it carries. Signature and semantics match DecodeNetFlowV9: flows
// that could be read come back, and data whose template is unknown is counted
// rather than treated as an error.
func DecodeIPFIX(b []byte, from netip.Addr, tc *TemplateCache, now time.Time) (*Packet, int, error) {
	if len(b) < ipfixHeaderLen {
		return nil, 0, ErrShortPacket{Want: ipfixHeaderLen, Got: len(b)}
	}
	if v := binary.BigEndian.Uint16(b[0:2]); v != 10 {
		return nil, 0, fmt.Errorf("flow: not an IPFIX message (version %d)", v)
	}
	// The declared length bounds the message. Trusting the datagram's own size
	// instead would read trailing bytes some middlebox appended; trusting the
	// field without checking would read past the buffer.
	msgLen := int(binary.BigEndian.Uint16(b[2:4]))
	if msgLen < ipfixHeaderLen {
		return nil, 0, fmt.Errorf("flow: IPFIX message length %d is shorter than its header", msgLen)
	}
	if msgLen > len(b) {
		return nil, 0, ErrShortPacket{Want: msgLen, Got: len(b)}
	}
	b = b[:msgLen]
	domainID := binary.BigEndian.Uint32(b[12:16])

	p := &Packet{ExporterIP: from}
	awaiting := 0

	off := ipfixHeaderLen
	for off+flowsetHeaderLen <= len(b) {
		setID := binary.BigEndian.Uint16(b[off : off+2])
		setLen := int(binary.BigEndian.Uint16(b[off+2 : off+4]))
		if setLen < flowsetHeaderLen || off+setLen > len(b) {
			return p, awaiting, fmt.Errorf("flow: IPFIX set length %d at offset %d does not fit", setLen, off)
		}
		body := b[off+flowsetHeaderLen : off+setLen]

		switch {
		case setID == ipfixSetTemplate:
			parseIPFIXTemplates(body, from, domainID, tc, now, false)
		case setID == ipfixSetOptionsTmpl:
			parseIPFIXTemplates(body, from, domainID, tc, now, true)
		case setID >= ipfixSetMinData:
			t, ok := tc.get(templateKey{from, domainID, setID, 10}, now)
			if !ok {
				awaiting++
				break
			}
			if t.isOptions() {
				readOptionsData(body, t, from, domainID, tc)
				break
			}
			p.Records = append(p.Records, readFlowData(body, t)...)
		default:
			// Set IDs 4-255 are reserved and carry nothing usable.
		}
		off += setLen
	}

	if iv := tc.getSampling(samplingKey{from, domainID}); iv > 1 {
		for i := range p.Records {
			p.Records[i].Bytes *= uint64(iv)
			p.Records[i].Packets *= uint64(iv)
			p.Records[i].Sampled = true
		}
	}
	return p, awaiting, nil
}

// parseIPFIXTemplates reads a Template Set or an Options Template Set. The two
// differ only in that the options form carries a scope field count, so one
// function handles both rather than duplicating the field loop — which is
// where the enterprise-number handling lives and is the part worth having in
// exactly one place.
func parseIPFIXTemplates(body []byte, from netip.Addr, domainID uint32, tc *TemplateCache, now time.Time, options bool) {
	off := 0
	for {
		header := 4
		if options {
			header = 6
		}
		if off+header > len(body) {
			return
		}
		id := binary.BigEndian.Uint16(body[off : off+2])
		fieldCount := int(binary.BigEndian.Uint16(body[off+2 : off+4]))
		scopeCount := 0
		if options {
			scopeCount = int(binary.BigEndian.Uint16(body[off+4 : off+6]))
		}
		off += header

		// A withdrawal carries a field count of zero. Nothing to store, and
		// reading on would treat the next template's header as fields.
		if fieldCount == 0 {
			continue
		}
		if options && (scopeCount == 0 || scopeCount > fieldCount) {
			return // an options template must scope at least one and at most all
		}

		t := &template{scopeFields: scopeCount, fields: make([]templateField, 0, fieldCount)}
		bad := false
		for i := 0; i < fieldCount; i++ {
			if off+4 > len(body) {
				return
			}
			raw := binary.BigEndian.Uint16(body[off : off+2])
			f := templateField{
				typ:    raw &^ enterpriseBit,
				length: binary.BigEndian.Uint16(body[off+2 : off+4]),
			}
			off += 4
			if raw&enterpriseBit != 0 {
				if off+4 > len(body) {
					return
				}
				f.enterprise = binary.BigEndian.Uint32(body[off : off+4])
				off += 4
			}
			if f.variable() {
				t.hasVarLen = true
			} else {
				t.recordLen += int(f.length)
			}
			if f.length == 0 {
				bad = true // a zero-width fixed field would never advance
			}
			t.fields = append(t.fields, f)
		}
		if bad || (!t.hasVarLen && t.recordLen == 0) {
			continue
		}
		// An options template with no scope is a plain template by another
		// name; isOptions is what keeps its records out of the flow table.
		tc.put(templateKey{from, domainID, id, 10}, t, now)
	}
}
