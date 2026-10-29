package flow

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// NetFlow v9 decoding.
//
// v5 is self-describing: fixed header, fixed 48-byte records, decode anything
// the moment it arrives. v9 is not. An exporter first sends a *template*
// naming the fields it will use and their widths, and every later data record
// is an opaque byte run that means nothing without it. That single difference
// is why v9 was not in the first increment: it turns a pure function into a
// stateful component with a cache, an expiry policy, a bound, and an answer for
// data that arrives before its template.
//
// The consequences are worth stating because they are operational, not
// theoretical:
//
//   - A collector restart loses every template. Exporters resend on their own
//     schedule — commonly every 10-20 minutes — so flow can be missing for
//     that long after a restart while nothing is wrong. The collector says so
//     rather than looking broken (see the awaiting-template counter).
//   - Templates are attacker-influenced state on an unauthenticated UDP port,
//     so the cache is bounded and expiring, like every other buffer here.
//   - A template can be redefined under the same ID. The newest definition
//     wins, because that is what the protocol says and what exporters do after
//     a configuration change.

const (
	netflowV9HeaderLen = 20
	// Template and options-template FlowSets have reserved IDs; anything from
	// 256 up is a data FlowSet naming the template it was encoded with.
	fsIDTemplate        = 0
	fsIDOptionsTemplate = 1
	fsIDMinData         = 256
	flowsetHeaderLen    = 4
)

// v9 field types this decoder understands. Everything else is skipped by its
// declared length, which is what lets an exporter send forty fields we do not
// care about without breaking the ones we do. The IDs 1-28 are shared with
// IPFIX's information elements, so one table serves both.
const (
	fInBytes    = 1
	fInPkts     = 2
	fProtocol   = 4
	fTOS        = 5
	fTCPFlags   = 6
	fSrcPort    = 7
	fSrcAddrV4  = 8
	fSrcMask    = 9
	fInputSNMP  = 10
	fDstPort    = 11
	fDstAddrV4  = 12
	fDstMask    = 13
	fOutputSNMP = 14
	fSrcAS      = 16
	fDstAS      = 17
	fOutBytes   = 23
	fOutPkts    = 24
	fSrcAddrV6  = 27
	fDstAddrV6  = 28
	fSrcMaskV6  = 29
	fDstMaskV6  = 30
	// Sampling reaches us two ways: inline on the flow record (rare), or in an
	// options data record that describes the sampler (usual).
	fSamplingInterval = 34
	fSamplerRandomInt = 50
	fSamplerInterval  = 305 // IPFIX samplingPacketInterval, seen from some v9 exporters

	// Fields kept for raw storage only (ADR-026). Shared numbering between v9
	// and IPFIX up to 127; the higher ones are IPFIX elements that v9
	// exporters reuse, and the 40000s are Cisco ASA's v9 extensions.
	fNextHopV4     = 15
	fBGPNextHopV4  = 18
	fLastSwitched  = 21
	fFirstSwitched = 22
	fICMPTypeV4    = 32
	fInSrcMAC      = 56
	fOutDstMAC     = 57
	fSrcVLAN       = 58
	fDstVLAN       = 59
	fDirection     = 61
	fNextHopV6     = 62
	fBGPNextHopV6  = 63
	fInDstMAC      = 80
	fOutSrcMAC     = 81
	fApplicationID = 95
	fAppName       = 96
	fICMPTypeV6    = 139
	fStartSeconds  = 150
	fEndSeconds    = 151
	fStartMillis   = 152
	fEndMillis     = 153
	fSysInitMillis = 160
	fNATSrcV4      = 225
	fNATDstV4      = 226
	fNATSrcPort    = 227
	fNATDstPort    = 228
	fFirewallEvent = 233
	fDot1qVLAN     = 243
	fNATSrcV6      = 281
	fNATDstV6      = 282
	fUserName      = 371
	fASAUserName   = 40000
	fASAFWEvent    = 40005
)

type templateField struct {
	typ    uint16
	length uint16
	// enterprise is non-zero for an IPFIX enterprise-specific element. Those
	// are vendor extensions with no registry-wide meaning, so they are skipped
	// by length rather than interpreted — a type ID only identifies a field
	// within its enterprise, and treating one as a standard IE would read some
	// vendor's private counter as a byte count.
	enterprise uint32
}

// varLength marks an IPFIX field whose width is carried in the record instead
// of the template (RFC 7011 §7). v9 has no such thing.
const varLength = 0xFFFF

func (f templateField) variable() bool { return f.length == varLength }

type template struct {
	fields []templateField
	// recordLen is the fixed record width, valid only when hasVarLen is false.
	recordLen int
	// hasVarLen means at least one field's width is per-record, so records
	// cannot be indexed by multiplication and must be walked in order.
	hasVarLen bool
	// scopeFields counts the leading fields that are scope rather than data.
	// Only options templates have them, and their presence is how a data
	// FlowSet is recognised as options data rather than flows.
	scopeFields int
	seen        time.Time
}

func (t *template) isOptions() bool { return t.scopeFields > 0 }

type templateKey struct {
	exporter netip.Addr
	sourceID uint32
	id       uint16
	// version separates v9 and IPFIX template spaces. An exporter running both
	// can legitimately use ID 300 for two different layouts, and sharing one
	// key would let each overwrite the other — misreading every record while
	// looking healthy.
	version uint16
}

// TemplateCache holds the field layouts exporters have announced.
//
// Bounded and expiring by construction. An unauthenticated UDP port that
// accepts "remember this layout" is a memory-growth primitive otherwise: a
// spoofed source can mint a new (exporter, sourceID, templateID) on every
// packet.
type TemplateCache struct {
	// TTL after which an unrefreshed template is forgotten. Zero means
	// DefaultTemplateTTL. Exporters resend periodically, so a live template is
	// continuously renewed and only a genuinely stale one expires.
	TTL time.Duration
	// Max templates held across all exporters. Zero means DefaultMaxTemplates.
	Max int

	mu        sync.Mutex
	templates map[templateKey]*template
	// sampling holds the interval most recently announced per exporter and
	// observation domain, learned from options data records.
	sampling map[samplingKey]uint32
}

type samplingKey struct {
	exporter netip.Addr
	sourceID uint32
}

const (
	// Long enough to survive an exporter that refreshes every 20 minutes with
	// room to lose one refresh, short enough that a decommissioned exporter's
	// entries do not outlive it by a working day.
	DefaultTemplateTTL  = 60 * time.Minute
	DefaultMaxTemplates = 10_000
)

func NewTemplateCache() *TemplateCache {
	return &TemplateCache{
		TTL: DefaultTemplateTTL, Max: DefaultMaxTemplates,
		templates: map[templateKey]*template{},
		sampling:  map[samplingKey]uint32{},
	}
}

func (tc *TemplateCache) ttl() time.Duration {
	if tc.TTL > 0 {
		return tc.TTL
	}
	return DefaultTemplateTTL
}

func (tc *TemplateCache) max() int {
	if tc.Max > 0 {
		return tc.Max
	}
	return DefaultMaxTemplates
}

func (tc *TemplateCache) put(k templateKey, t *template, now time.Time) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.templates == nil {
		tc.templates = map[templateKey]*template{}
	}
	if _, replacing := tc.templates[k]; !replacing && len(tc.templates) >= tc.max() {
		// Make room by dropping what has already expired before refusing.
		tc.expireLocked(now)
		if len(tc.templates) >= tc.max() {
			return // full of live templates: refuse rather than evict a working one
		}
	}
	t.seen = now
	tc.templates[k] = t
}

func (tc *TemplateCache) get(k templateKey, now time.Time) (*template, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	t, ok := tc.templates[k]
	if !ok {
		return nil, false
	}
	if now.Sub(t.seen) > tc.ttl() {
		delete(tc.templates, k)
		return nil, false
	}
	return t, true
}

func (tc *TemplateCache) expireLocked(now time.Time) {
	for k, t := range tc.templates {
		if now.Sub(t.seen) > tc.ttl() {
			delete(tc.templates, k)
		}
	}
}

// Len reports how many templates are held, for the intake report.
func (tc *TemplateCache) Len() int {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return len(tc.templates)
}

func (tc *TemplateCache) putSampling(k samplingKey, interval uint32) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.sampling == nil {
		tc.sampling = map[samplingKey]uint32{}
	}
	// One entry per (exporter, domain), so this cannot grow beyond the
	// template cap's own key space.
	tc.sampling[k] = interval
}

func (tc *TemplateCache) getSampling(k samplingKey) uint32 {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.sampling[k]
}

// ErrAwaitingTemplate means a data FlowSet arrived whose template has not been
// seen yet. It is expected after a restart and not an error in the packet.
type ErrAwaitingTemplate struct {
	TemplateID uint16
}

func (e ErrAwaitingTemplate) Error() string {
	return fmt.Sprintf("flow: no template %d yet", e.TemplateID)
}

// DecodeNetFlowV9 parses a v9 export packet against the template cache,
// learning any templates it carries.
//
// A packet can hold templates, flow data and options data at once, so this
// returns whatever flows it could decode *and* an awaiting-template signal if
// some data went unread — the two are not exclusive, and treating the packet as
// all-or-nothing would discard usable flows during the window after a restart.
func DecodeNetFlowV9(b []byte, from netip.Addr, tc *TemplateCache, now time.Time) (*Packet, int, error) {
	if len(b) < netflowV9HeaderLen {
		return nil, 0, ErrShortPacket{Want: netflowV9HeaderLen, Got: len(b)}
	}
	if v := binary.BigEndian.Uint16(b[0:2]); v != 9 {
		return nil, 0, fmt.Errorf("flow: not a NetFlow v9 packet (version %d)", v)
	}
	sourceID := binary.BigEndian.Uint32(b[16:20])
	clock := exportClock{
		uptimeMs: binary.BigEndian.Uint32(b[4:8]),
		unix:     time.Unix(int64(binary.BigEndian.Uint32(b[8:12])), 0),
	}
	p := &Packet{ExporterIP: from}
	awaiting := 0

	off := netflowV9HeaderLen
	for off+flowsetHeaderLen <= len(b) {
		fsID := binary.BigEndian.Uint16(b[off : off+2])
		fsLen := int(binary.BigEndian.Uint16(b[off+2 : off+4]))
		// A FlowSet shorter than its own header, or longer than the datagram,
		// would either loop forever or read past the end.
		if fsLen < flowsetHeaderLen || off+fsLen > len(b) {
			return p, awaiting, fmt.Errorf("flow: v9 flowset length %d at offset %d does not fit", fsLen, off)
		}
		body := b[off+flowsetHeaderLen : off+fsLen]

		switch {
		case fsID == fsIDTemplate:
			parseTemplates(body, from, sourceID, tc, now)
		case fsID == fsIDOptionsTemplate:
			parseOptionsTemplates(body, from, sourceID, tc, now)
		case fsID >= fsIDMinData:
			t, ok := tc.get(templateKey{from, sourceID, fsID, 9}, now)
			if !ok {
				awaiting++
				break
			}
			if t.isOptions() {
				readOptionsData(body, t, from, sourceID, tc)
				break
			}
			p.Records = append(p.Records, readFlowData(body, t, clock, "netflow9")...)
		default:
			// FlowSet IDs 2-255 are reserved and carry nothing we can use.
		}
		off += fsLen
	}

	// Sampling is applied after decoding, because the options record that
	// declares it may arrive in the same packet as the flows it describes.
	applySampling(p, tc.getSampling(samplingKey{from, sourceID}))
	return p, awaiting, nil
}

// applySampling scales a packet's records by the exporter's announced
// interval. A record that carried its own inline rate was already scaled by
// readFlowData and is left alone — scaling it twice would multiply by N².
func applySampling(p *Packet, iv uint32) {
	for i := range p.Records {
		r := &p.Records[i]
		if r.SamplingRate > 1 {
			continue
		}
		if iv > 1 {
			r.Bytes *= uint64(iv)
			r.Packets *= uint64(iv)
			r.Sampled = true
			r.SamplingRate = iv
		} else {
			r.SamplingRate = 1
		}
	}
}

func parseTemplates(body []byte, from netip.Addr, sourceID uint32, tc *TemplateCache, now time.Time) {
	off := 0
	for off+4 <= len(body) {
		id := binary.BigEndian.Uint16(body[off : off+2])
		count := int(binary.BigEndian.Uint16(body[off+2 : off+4]))
		off += 4
		if count <= 0 || off+count*4 > len(body) {
			return // truncated or absurd: stop rather than read past the set
		}
		t := &template{fields: make([]templateField, 0, count)}
		for i := 0; i < count; i++ {
			f := templateField{
				typ:    binary.BigEndian.Uint16(body[off : off+2]),
				length: binary.BigEndian.Uint16(body[off+2 : off+4]),
			}
			off += 4
			t.fields = append(t.fields, f)
			t.recordLen += int(f.length)
		}
		if t.recordLen == 0 {
			continue // a template describing nothing would divide by zero later
		}
		tc.put(templateKey{from, sourceID, id, 9}, t, now)
	}
}

func parseOptionsTemplates(body []byte, from netip.Addr, sourceID uint32, tc *TemplateCache, now time.Time) {
	off := 0
	for off+6 <= len(body) {
		id := binary.BigEndian.Uint16(body[off : off+2])
		// Both lengths are in *bytes of field definitions*, not field counts —
		// a reliable place to go wrong, since every other count in v9 is a
		// number of fields.
		scopeBytes := int(binary.BigEndian.Uint16(body[off+2 : off+4]))
		optionBytes := int(binary.BigEndian.Uint16(body[off+4 : off+6]))
		off += 6
		if scopeBytes%4 != 0 || optionBytes%4 != 0 ||
			off+scopeBytes+optionBytes > len(body) {
			return
		}
		t := &template{scopeFields: scopeBytes / 4}
		for end := off + scopeBytes + optionBytes; off+4 <= end; off += 4 {
			f := templateField{
				typ:    binary.BigEndian.Uint16(body[off : off+2]),
				length: binary.BigEndian.Uint16(body[off+2 : off+4]),
			}
			t.fields = append(t.fields, f)
			t.recordLen += int(f.length)
		}
		if t.recordLen == 0 || t.scopeFields == 0 {
			continue
		}
		tc.put(templateKey{from, sourceID, id, 9}, t, now)
		// Options templates are padded to a 4-byte boundary; the loop's own
		// bounds check handles the remainder.
	}
}

// eachRecord walks a data set, calling fn with one record's field values.
//
// Two shapes in one function because IPFIX allows a field whose width lives in
// the record rather than the template. A fixed-width record can be indexed by
// multiplication; a variable one has to be walked, and a walker that assumed
// otherwise would slide off the field boundary and read every subsequent
// record as garbage that still parses.
func eachRecord(body []byte, t *template, fn func(vals [][]byte)) {
	if len(t.fields) == 0 {
		return
	}
	if !t.hasVarLen {
		if t.recordLen <= 0 {
			return
		}
		vals := make([][]byte, len(t.fields))
		for i := 0; i+t.recordLen <= len(body); i += t.recordLen {
			rec := body[i : i+t.recordLen]
			off := 0
			for j, f := range t.fields {
				vals[j] = rec[off : off+int(f.length)]
				off += int(f.length)
			}
			fn(vals)
		}
		return
	}

	// Variable-length: RFC 7011 §7 encodes the width in the record, as one
	// byte, or 255 followed by two bytes when the value is 255 bytes or longer.
	off := 0
	for off < len(body) {
		start := off
		vals := make([][]byte, len(t.fields))
		ok := true
		for j, f := range t.fields {
			width := int(f.length)
			if f.variable() {
				if off >= len(body) {
					ok = false
					break
				}
				width = int(body[off])
				off++
				if width == 255 {
					if off+2 > len(body) {
						ok = false
						break
					}
					width = int(binary.BigEndian.Uint16(body[off : off+2]))
					off += 2
				}
			}
			if off+width > len(body) {
				ok = false
				break
			}
			vals[j] = body[off : off+width]
			off += width
		}
		if !ok || off == start {
			return // truncated, or a record that consumed nothing: stop
		}
		fn(vals)
	}
}

// readFlowData turns a data set into records using its template. clock
// converts v9's uptime-relative timestamps; typ names the format for storage.
func readFlowData(body []byte, t *template, clock exportClock, typ string) []Record {
	var out []Record
	eachRecord(body, t, func(vals [][]byte) {
		r := Record{Type: typ}
		var inlineSampling uint64
		// Relative timestamps are resolved after the loop, because IPFIX's
		// systemInitTimeMilliseconds may come after them in the template.
		var firstUp, lastUp, sysInit uint64
		for i, f := range t.fields {
			if f.enterprise != 0 {
				continue // vendor-private: no registry meaning, skip by length
			}
			val := vals[i]
			switch f.typ {
			case fInBytes, fOutBytes:
				r.Bytes += beUint(val)
			case fInPkts, fOutPkts:
				r.Packets += beUint(val)
			case fProtocol:
				if len(val) > 0 {
					r.Protocol = val[len(val)-1]
				}
			case fSrcPort:
				r.SrcPort = narrow16(beUint(val))
			case fDstPort:
				r.DstPort = narrow16(beUint(val))
			case fInputSNMP:
				r.InputIf = narrow32(beUint(val))
			case fOutputSNMP:
				r.OutputIf = narrow32(beUint(val))
			case fSrcAddrV4, fSrcAddrV6:
				if a, ok := netip.AddrFromSlice(val); ok {
					r.SrcAddr = a.Unmap()
				}
			case fDstAddrV4, fDstAddrV6:
				if a, ok := netip.AddrFromSlice(val); ok {
					r.DstAddr = a.Unmap()
				}
			case fTOS:
				if len(val) > 0 {
					r.TOS = val[len(val)-1]
				}
			case fTCPFlags:
				// IPFIX tcpControlBits is 16 bits wide; the classic flags are
				// its low byte either way.
				if len(val) > 0 {
					r.TCPFlags = val[len(val)-1]
				}
			case fSrcMask, fSrcMaskV6:
				r.SrcMask = narrow8(beUint(val))
			case fDstMask, fDstMaskV6:
				r.DstMask = narrow8(beUint(val))
			case fSrcAS:
				r.SrcAS = narrow32(beUint(val))
			case fDstAS:
				r.DstAS = narrow32(beUint(val))
			case fSamplingInterval, fSamplerRandomInt, fSamplerInterval:
				// Inline sampling on the flow record itself. Rare, but when it
				// is here it is authoritative for this record. Applied after
				// the loop: a template may list it before the counters, and
				// scaling here would then scale zero and miss the real values.
				inlineSampling = beUint(val)
			default:
				readRawField(&r, f.typ, val, &firstUp, &lastUp, &sysInit)
			}
		}
		if inlineSampling > 1 && inlineSampling <= 0xFFFFFFFF {
			r.Bytes *= inlineSampling
			r.Packets *= inlineSampling
			r.Sampled = true
			r.SamplingRate = uint32(inlineSampling)
		}
		switch {
		case sysInit > 0:
			// IPFIX: relative times count from the exporter's boot instant.
			boot := time.UnixMilli(int64(sysInit)) //nolint:gosec // ms since 1970 fits
			if firstUp > 0 {
				r.Start = boot.Add(time.Duration(narrow32(firstUp)) * time.Millisecond).UTC()
			}
			if lastUp > 0 {
				r.End = boot.Add(time.Duration(narrow32(lastUp)) * time.Millisecond).UTC()
			}
		case r.Start.IsZero() && r.End.IsZero():
			r.Start = clock.at(narrow32(firstUp))
			r.End = clock.at(narrow32(lastUp))
		}
		// A record with no addresses is not a flow — most likely a template
		// mismatch, where the layout parsed but described something else.
		if !r.SrcAddr.IsValid() || !r.DstAddr.IsValid() {
			return
		}
		out = append(out, r)
	})
	return out
}

// readOptionsData looks for a sampling interval in an options data record.
//
// Without this, a sampled v9 exporter reports one packet in N and NetInv
// charts it as the whole truth — under-reporting by the sampling rate with no
// error anywhere. That is the same class of silent wrongness the v5 decoder
// guards against, arriving by a different route.
func readOptionsData(body []byte, t *template, from netip.Addr, sourceID uint32, tc *TemplateCache) {
	eachRecord(body, t, func(vals [][]byte) {
		for i, f := range t.fields {
			if f.enterprise != 0 {
				continue
			}
			switch f.typ {
			case fSamplingInterval, fSamplerRandomInt, fSamplerInterval:
				if iv := beUint(vals[i]); iv > 0 {
					tc.putSampling(samplingKey{from, sourceID}, narrow32(iv))
				}
			}
		}
	})
}

// narrow16 and narrow32 bring an exporter-declared value down to the width the
// protocol actually defines for the field.
//
// Field widths are the exporter's choice, so nothing stops a template from
// declaring an 8-byte L4_SRC_PORT. A plain conversion would keep the low bytes
// and produce a plausible wrong port with no indication anything happened —
// silent truncation driven by data from an unauthenticated socket. A value
// that does not fit is not a large port, it is a misdescribed field, so it
// becomes zero: unknown, which the rest of the pipeline already handles (an
// ifIndex of 0 is unattributable and the flow is dropped).
func narrow8(v uint64) uint8 {
	if v > 0xFF {
		return 0
	}
	return uint8(v)
}

func narrow16(v uint64) uint16 {
	if v > 0xFFFF {
		return 0
	}
	return uint16(v)
}

func narrow32(v uint64) uint32 {
	if v > 0xFFFFFFFF {
		return 0
	}
	return uint32(v)
}

// beUint reads a big-endian unsigned integer of whatever width the template
// declared. v9 field widths are the exporter's choice — INPUT_SNMP is 2 bytes
// on one platform and 4 on another — so nothing may assume a size.
func beUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// readRawField fills the fields only raw storage uses (ADR-026). Kept apart
// from readFlowData's switch so that one stays about what the aggregates need.
// Relative timestamps are returned through the pointers, because what they
// are relative to is only known once the whole record has been read.
func readRawField(r *Record, typ uint16, val []byte, firstUp, lastUp, sysInit *uint64) {
	switch typ {
	case fFirstSwitched:
		*firstUp = beUint(val)
	case fLastSwitched:
		*lastUp = beUint(val)
	case fSysInitMillis:
		*sysInit = beUint(val)
	case fStartSeconds:
		r.Start = unixAt(beUint(val), time.Second)
	case fEndSeconds:
		r.End = unixAt(beUint(val), time.Second)
	case fStartMillis:
		r.Start = unixAt(beUint(val), time.Millisecond)
	case fEndMillis:
		r.End = unixAt(beUint(val), time.Millisecond)
	case fInSrcMAC, fOutSrcMAC:
		if len(val) == 6 && r.SrcMAC == 0 {
			r.SrcMAC = beUint(val)
		}
	case fInDstMAC, fOutDstMAC:
		if len(val) == 6 && r.DstMAC == 0 {
			r.DstMAC = beUint(val)
		}
	case fSrcVLAN, fDot1qVLAN:
		r.SrcVLAN = narrow16(beUint(val)) & 0x0FFF
	case fDstVLAN:
		r.DstVLAN = narrow16(beUint(val)) & 0x0FFF
	case fNextHopV4, fNextHopV6:
		r.NextHop = addrOrZero(val)
	case fBGPNextHopV4, fBGPNextHopV6:
		r.BGPNextHop = addrOrZero(val)
	case fDirection:
		if len(val) > 0 && val[len(val)-1] <= 1 {
			r.Direction = val[len(val)-1] + 1
		}
	case fICMPTypeV4, fICMPTypeV6:
		r.ICMPType = narrow16(beUint(val))
	case fApplicationID:
		r.AppID = appID(val)
	case fAppName:
		r.AppName = text(val)
	case fNATSrcV4, fNATSrcV6:
		r.NATSrcAddr = addrOrZero(val)
	case fNATDstV4, fNATDstV6:
		r.NATDstAddr = addrOrZero(val)
	case fNATSrcPort:
		r.NATSrcPort = narrow16(beUint(val))
	case fNATDstPort:
		r.NATDstPort = narrow16(beUint(val))
	case fFirewallEvent, fASAFWEvent:
		r.FWEvent = narrow8(beUint(val))
	case fUserName, fASAUserName:
		r.User = text(val)
	}
}

// unixAt reads an absolute timestamp in the given unit; zero stays zero.
func unixAt(v uint64, unit time.Duration) time.Time {
	if v == 0 || v > uint64(1<<62)/uint64(unit) { //nolint:gosec // unit is a positive constant
		return time.Time{}
	}
	return time.Unix(0, int64(v)*int64(unit)).UTC() //nolint:gosec // bounded above
}

// addrOrZero reads a 4- or 16-byte address; anything else, or the unspecified
// address an exporter sends for "none", is no address.
func addrOrZero(b []byte) netip.Addr {
	a, ok := netip.AddrFromSlice(b)
	if !ok || a.IsUnspecified() {
		return netip.Addr{}
	}
	return a.Unmap()
}

// appID renders RFC 6759's applicationId: one byte naming the classification
// engine, then the selector. A selector up to 8 bytes is a number; a longer
// one is shown in hex rather than guessed at.
func appID(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	if len(b) <= 9 {
		return fmt.Sprintf("%d:%d", b[0], beUint(b[1:]))
	}
	return fmt.Sprintf("%d:%x", b[0], b[1:])
}

// text reads a fixed- or variable-width string field: exporters pad fixed
// widths with NULs, and the value comes off an unauthenticated socket, so it
// is cut at 128 bytes and kept to valid UTF-8.
func text(b []byte) string {
	for i, c := range b {
		if c == 0 {
			b = b[:i]
			break
		}
	}
	if len(b) > 128 {
		b = b[:128]
	}
	return strings.ToValidUTF8(string(b), "")
}
