package flow

import (
	"net/netip"
	"sort"
	"sync"
	"time"
)

// Dimension is what a set of counters is keyed by.
type Dimension string

const (
	// DimTalker is a single host, counted in whichever direction it appeared.
	DimTalker Dimension = "talker"
	// DimConversation is an unordered host pair, so A→B and B→A accumulate
	// together — an operator asking "what is this link carrying" means the
	// exchange, not each half of it.
	DimConversation Dimension = "conversation"
	// DimApplication is the well-known port and protocol, which is the
	// closest thing to "what application" available without deep inspection.
	DimApplication Dimension = "application"
)

// Key identifies one aggregated bucket. Everything in it becomes a metric
// label, so every field here must be bounded — which is exactly why the
// aggregate is cut to top-N before it is written.
type Key struct {
	ExporterIP string
	IfIndex    uint32
	Dimension  Dimension
	Value      string
}

type counters struct {
	bytes   uint64
	packets uint64
	sampled bool
}

// Aggregator accumulates flow records into per-interface top-N tables.
//
// Bounded by construction: memory is (exporters × interfaces × dimensions ×
// distinct values) for one interval only, and Drain resets it. The top-N cut
// happens on the way out, so a conversation that never makes the cut costs one
// map entry for one interval and is then forgotten — it is not stored and
// cannot be recovered later.
type Aggregator struct {
	// TopN kept per interface per dimension. Zero means DefaultTopN.
	TopN int

	mu     sync.Mutex
	counts map[Key]*counters
	// guard caps distinct keys per interval so a scan or a spoofed source
	// cannot grow the map without limit before Drain runs. Records past the
	// cap are counted into an "other" bucket rather than dropped silently.
	MaxKeys int
	dropped uint64
}

const (
	DefaultTopN    = 10
	DefaultMaxKeys = 200_000
	// OtherValue collects everything past MaxKeys, so the total stays honest
	// even when the detail is gone.
	OtherValue = "other"
)

func NewAggregator() *Aggregator {
	return &Aggregator{TopN: DefaultTopN, MaxKeys: DefaultMaxKeys, counts: map[Key]*counters{}}
}

// Add folds one packet's records into the current interval.
func (a *Aggregator) Add(p *Packet) {
	if p == nil {
		return
	}
	exporter := p.ExporterIP.String()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.counts == nil {
		a.counts = map[Key]*counters{}
	}
	for _, r := range p.Records {
		// Attribute to the ingress interface where there is one. A flow with
		// neither is unattributable and would otherwise pile into ifIndex 0,
		// which reads as a real interface on the dashboard.
		ifIndex := r.InputIf
		if ifIndex == 0 {
			ifIndex = r.OutputIf
		}
		if ifIndex == 0 {
			continue
		}
		a.add(Key{exporter, ifIndex, DimTalker, r.SrcAddr.String()}, r)
		a.add(Key{exporter, ifIndex, DimTalker, r.DstAddr.String()}, r)
		a.add(Key{exporter, ifIndex, DimConversation, conversation(r.SrcAddr, r.DstAddr)}, r)
		a.add(Key{exporter, ifIndex, DimApplication, application(r)}, r)
	}
}

func (a *Aggregator) add(k Key, r Record) {
	c, ok := a.counts[k]
	if !ok {
		if a.MaxKeys > 0 && len(a.counts) >= a.MaxKeys {
			k.Value = OtherValue
			c, ok = a.counts[k]
			if !ok {
				c = &counters{}
				a.counts[k] = c
			}
			a.dropped++
		} else {
			c = &counters{}
			a.counts[k] = c
		}
	}
	c.bytes += r.Bytes
	c.packets += r.Packets
	c.sampled = c.sampled || r.Sampled
}

// Bucket is one row of the drained result.
type Bucket struct {
	Key     Key
	Bytes   uint64
	Packets uint64
	Sampled bool
}

// Drain returns the top-N buckets per (exporter, interface, dimension) and
// resets the accumulator. Ties break on the value so repeated drains of
// identical data produce identical output, which keeps tests meaningful.
func (a *Aggregator) Drain() []Bucket {
	a.mu.Lock()
	counts := a.counts
	a.counts = map[Key]*counters{}
	a.dropped = 0
	topN := a.TopN
	a.mu.Unlock()

	if topN <= 0 {
		topN = DefaultTopN
	}

	grouped := map[Key][]Bucket{}
	for k, c := range counts {
		g := Key{ExporterIP: k.ExporterIP, IfIndex: k.IfIndex, Dimension: k.Dimension}
		grouped[g] = append(grouped[g], Bucket{
			Key: k, Bytes: c.bytes, Packets: c.packets, Sampled: c.sampled,
		})
	}
	out := make([]Bucket, 0, len(grouped)*topN)
	for _, rows := range grouped {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Bytes != rows[j].Bytes {
				return rows[i].Bytes > rows[j].Bytes
			}
			return rows[i].Key.Value < rows[j].Key.Value
		})
		if len(rows) > topN {
			rows = rows[:topN]
		}
		out = append(out, rows...)
	}
	return out
}

// Dropped reports how many records have been folded into the "other" bucket in
// the interval currently accumulating — a signal that the network is doing
// something the top-N view will not describe well. Drain resets it, so read it
// before draining, not after.
func (a *Aggregator) Dropped() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dropped
}

// conversation orders the pair so both directions land in one bucket.
func conversation(a, b netip.Addr) string {
	if a.Compare(b) <= 0 {
		return a.String() + " ⇄ " + b.String()
	}
	return b.String() + " ⇄ " + a.String()
}

// application names the well-known side of the exchange. The lower port is a
// good proxy for the service: an ephemeral client port is high and arbitrary,
// so keying on it would produce one bucket per connection.
func application(r Record) string {
	port := r.SrcPort
	if r.DstPort < port || port == 0 {
		port = r.DstPort
	}
	name := protoName(r.Protocol)
	if port == 0 {
		return name
	}
	if svc, ok := wellKnown[portProto{port, r.Protocol}]; ok {
		return svc
	}
	return name + "/" + itoa(uint64(port))
}

type portProto struct {
	port  uint16
	proto uint8
}

// Only names worth showing an operator. An exhaustive services list would add
// noise for ports nobody recognises anyway, and the numeric fallback is
// perfectly readable.
var wellKnown = map[portProto]string{
	{22, 6}: "ssh", {23, 6}: "telnet", {25, 6}: "smtp", {53, 6}: "dns",
	{53, 17}: "dns", {80, 6}: "http", {110, 6}: "pop3", {123, 17}: "ntp",
	{143, 6}: "imap", {161, 17}: "snmp", {389, 6}: "ldap", {443, 6}: "https",
	{443, 17}: "quic", {445, 6}: "smb", {465, 6}: "smtps", {514, 17}: "syslog",
	{587, 6}: "submission", {993, 6}: "imaps", {995, 6}: "pop3s",
	{1194, 17}: "openvpn", {3306, 6}: "mysql", {3389, 6}: "rdp",
	{5432, 6}: "postgres", {5060, 17}: "sip", {6379, 6}: "redis",
	{51820, 17}: "wireguard",
}

func protoName(p uint8) string {
	switch p {
	case 1:
		return "icmp"
	case 6:
		return "tcp"
	case 17:
		return "udp"
	case 47:
		return "gre"
	case 50:
		return "esp"
	case 58:
		return "icmp6"
	default:
		return "ip/" + itoa(uint64(p))
	}
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// Interval is the aggregation window. It matches the shortest poll cadence so
// flow series and interface counters line up on a chart without one of them
// being interpolated.
const Interval = time.Minute
