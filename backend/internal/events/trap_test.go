package events

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func v2Packet(t *testing.T, community string, pdu gosnmp.PDUType, trapOID string, vars ...gosnmp.SnmpPDU) []byte {
	t.Helper()
	p := &gosnmp.SnmpPacket{
		Version: gosnmp.Version2c, Community: community, PDUType: pdu, RequestID: 42,
		Variables: append([]gosnmp.SnmpPDU{
			{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(1234)},
			{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: trapOID},
		}, vars...),
	}
	b, err := p.MarshalMsg()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func linkDown(t *testing.T, community string) []byte {
	return v2Packet(t, community, gosnmp.SNMPv2Trap, ".1.3.6.1.6.3.1.1.5.3",
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.1.7", Type: gosnmp.Integer, Value: 7},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.7.7", Type: gosnmp.Integer, Value: 1},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.8.7", Type: gosnmp.Integer, Value: 2},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.31.1.1.1.18.7", Type: gosnmp.OctetString, Value: []byte("uplink")},
	)
}

func TestDecodeV2cLinkDown(t *testing.T) {
	src := netip.MustParseAddr("192.0.2.10")
	tr, community, err := Decode(linkDown(t, "public"), src, t0)
	if err != nil {
		t.Fatal(err)
	}
	if community != "public" || tr.Version != "2c" || tr.Inform {
		t.Errorf("community=%q version=%q inform=%v", community, tr.Version, tr.Inform)
	}
	if tr.TrapOID != "1.3.6.1.6.3.1.1.5.3" || tr.Label() != "linkDown" {
		t.Errorf("trap oid %q label %q", tr.TrapOID, tr.Label())
	}
	if tr.IfIndex != "7" {
		t.Errorf("if_index = %q, want 7 from the ifIndex varbind", tr.IfIndex)
	}
	// sysUpTime and snmpTrapOID are the envelope, not content.
	if len(tr.Varbinds) != 4 {
		t.Fatalf("varbinds = %d, want 4: %+v", len(tr.Varbinds), tr.Varbinds)
	}
	if v := tr.Varbinds[3]; v.OID != "1.3.6.1.2.1.31.1.1.1.18.7" || v.Value != "uplink" {
		t.Errorf("ifAlias varbind = %+v", v)
	}
}

// v1 generic traps map onto the v2 snmpTraps OIDs (RFC 3584 §3.1), so one
// rule covers both versions; enterprise-specific ones become enterprise.0.n.
func TestDecodeV1MapsOntoV2TrapOIDs(t *testing.T) {
	for _, c := range []struct {
		generic, specific int
		enterprise, want  string
	}{
		{4, 0, ".1.3.6.1.4.1.9", "1.3.6.1.6.3.1.1.5.5"}, // authenticationFailure
		{6, 17, ".1.3.6.1.4.1.12356.101", "1.3.6.1.4.1.12356.101.0.17"},
	} {
		p := &gosnmp.SnmpPacket{
			Version: gosnmp.Version1, Community: "public", PDUType: gosnmp.Trap,
			SnmpTrap: gosnmp.SnmpTrap{
				Enterprise: c.enterprise, AgentAddress: "198.51.100.7",
				GenericTrap: c.generic, SpecificTrap: c.specific, Timestamp: 100,
				Variables: []gosnmp.SnmpPDU{
					{Name: ".1.3.6.1.2.1.1.5.0", Type: gosnmp.OctetString, Value: []byte("fw1")},
				},
			},
		}
		p.Variables = p.SnmpTrap.Variables
		b, err := p.MarshalMsg()
		if err != nil {
			t.Fatalf("marshal v1: %v", err)
		}
		tr, _, err := Decode(b, netip.MustParseAddr("192.0.2.1"), t0)
		if err != nil {
			t.Fatalf("decode v1: %v", err)
		}
		if tr.Version != "1" || tr.TrapOID != c.want {
			t.Errorf("generic %d: version %q oid %q, want %q", c.generic, tr.Version, tr.TrapOID, c.want)
		}
		if tr.Agent != netip.MustParseAddr("198.51.100.7") {
			t.Errorf("agent-addr = %v", tr.Agent)
		}
	}
}

func TestDecodeRejectsV3AsUnsupportedNotMalformed(t *testing.T) {
	// A v3 header: SEQUENCE, INTEGER version 3. The rest is irrelevant — the
	// version must be recognised before anything tries to decode USM.
	b := []byte{0x30, 0x10, 0x02, 0x01, 0x03, 0x30, 0x00}
	if _, _, err := Decode(b, netip.Addr{}, t0); err != ErrUnsupportedVersion {
		t.Errorf("err = %v, want ErrUnsupportedVersion", err)
	}
	if PeekVersion([]byte("PROBE")) != -1 {
		t.Error("non-SNMP bytes read as a version")
	}
}

func TestInformIsAcknowledgedWithSameRequestID(t *testing.T) {
	b := v2Packet(t, "public", gosnmp.InformRequest, ".1.3.6.1.4.1.25461.2.1.3.2.0.1")
	tr, _, err := Decode(b, netip.MustParseAddr("192.0.2.1"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Inform {
		t.Fatal("inform not recognised")
	}
	ack, err := tr.Ack()
	if err != nil || ack == nil {
		t.Fatalf("ack = %v, %v", ack, err)
	}
	g := &gosnmp.GoSNMP{Logger: quiet}
	resp, err := g.SnmpDecodePacket(ack)
	if err != nil {
		t.Fatalf("ack does not decode: %v", err)
	}
	if resp.PDUType != gosnmp.GetResponse || resp.RequestID != 42 {
		t.Errorf("ack pdu %v request-id %d, want GetResponse/42", resp.PDUType, resp.RequestID)
	}
	if tr.Label() != "1.3.6.1.4.1.25461.2.1.3.2.0.1" {
		t.Errorf("vendor trap label = %q, want the numeric OID", tr.Label())
	}
}

// ---- listener ----

type fakeStore struct {
	mu    sync.Mutex
	traps []*Trap
}

func (f *fakeStore) InsertTraps(_ context.Context, t []*Trap) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.traps = append(f.traps, t...)
	return nil
}

type fakePub struct{ samples []wire.Sample }

func (f *fakePub) PublishSamples(_ context.Context, s []wire.Sample) error {
	f.samples = append(f.samples, s...)
	return nil
}

type fakeResolver map[netip.Addr]string

func (f fakeResolver) DeviceByAddr(a netip.Addr) string { return f[a] }

func udp(a string) net.Addr {
	return &net.UDPAddr{IP: net.ParseIP(a), Port: 50000}
}

func newListener() (*Listener, *fakeStore, *fakePub) {
	st, pub := &fakeStore{}, &fakePub{}
	l := &Listener{
		Resolve: fakeResolver{netip.MustParseAddr("192.0.2.10"): "dev_fw"},
		Store:   st, Publish: pub, Log: discardLog(),
	}
	l.defaults()
	return l, st, pub
}

func drain(l *Listener) []*Trap {
	var out []*Trap
	for {
		select {
		case t := <-l.queue:
			out = append(out, t)
		default:
			return out
		}
	}
}

func TestKnownDeviceTrapIsStoredAndPublishedWithIfIndex(t *testing.T) {
	l, st, pub := newListener()
	l.Handle(linkDown(t, "public"), udp("192.0.2.10"), t0)
	l.write(context.Background(), drain(l))

	if len(st.traps) != 1 || st.traps[0].DeviceID != "dev_fw" {
		t.Fatalf("stored %+v", st.traps)
	}
	if len(pub.samples) != 1 {
		t.Fatalf("published %d samples, want 1", len(pub.samples))
	}
	s := pub.samples[0]
	if s.Name != MetricName || s.DeviceID != "dev_fw" || s.Value != 1 ||
		s.Labels["trap"] != "linkDown" || s.Labels["if_index"] != "7" ||
		s.TSMillis != t0.UnixMilli() {
		t.Errorf("sample = %+v", s)
	}
}

// An unknown source is kept where an operator can see it, but writes no
// series: otherwise any address past the allow-list could mint metrics.
func TestUnknownSourceIsStoredButWritesNoSeries(t *testing.T) {
	l, st, pub := newListener()
	l.Handle(linkDown(t, "public"), udp("203.0.113.99"), t0)
	l.write(context.Background(), drain(l))
	if len(st.traps) != 1 || st.traps[0].DeviceID != "" {
		t.Errorf("stored %+v", st.traps)
	}
	if len(pub.samples) != 0 {
		t.Errorf("unknown source published %d samples", len(pub.samples))
	}
	if l.stats.unknownSource.Load() != 1 {
		t.Error("unknown source not counted")
	}
}

func TestAllowListAndCommunityAreEnforced(t *testing.T) {
	l, _, _ := newListener()
	l.Allow = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	l.Communities = map[string]bool{"s3cret": true}

	l.Handle(linkDown(t, "s3cret"), udp("198.51.100.1"), t0) // outside allow-list
	l.Handle(linkDown(t, "public"), udp("192.0.2.10"), t0)   // wrong community
	l.Handle(linkDown(t, "s3cret"), udp("192.0.2.10"), t0)   // accepted

	if got := len(drain(l)); got != 1 {
		t.Errorf("queued %d traps, want 1", got)
	}
	if l.stats.refused.Load() != 1 || l.stats.badCommunity.Load() != 1 {
		t.Errorf("refused=%d bad_community=%d", l.stats.refused.Load(), l.stats.badCommunity.Load())
	}
}

func TestPerSourceRateLimit(t *testing.T) {
	l, _, _ := newListener()
	l.limiter = newLimiter(1, 3)
	b := linkDown(t, "public")
	for i := 0; i < 10; i++ {
		l.Handle(b, udp("192.0.2.10"), t0) // same instant: only the burst passes
	}
	l.Handle(b, udp("192.0.2.11"), t0) // another source has its own bucket
	if got := len(drain(l)); got != 4 {
		t.Errorf("accepted %d, want 3 from the burst + 1 from the other source", got)
	}
	if l.stats.rateLimited.Load() != 7 {
		t.Errorf("rate limited = %d, want 7", l.stats.rateLimited.Load())
	}
	l.Handle(b, udp("192.0.2.10"), t0.Add(2*time.Second)) // refilled
	if got := len(drain(l)); got != 1 {
		t.Errorf("after refill accepted %d, want 1", got)
	}
}

func TestTrapLabelsAreCappedPerDevice(t *testing.T) {
	l, _, pub := newListener()
	l.labels.max = 2
	for _, oid := range []string{".1.3.6.1.4.1.9.0.1", ".1.3.6.1.4.1.9.0.2", ".1.3.6.1.4.1.9.0.3"} {
		l.Handle(v2Packet(t, "public", gosnmp.SNMPv2Trap, oid), udp("192.0.2.10"), t0)
	}
	l.write(context.Background(), drain(l))
	if got := pub.samples[2].Labels["trap"]; got != "other" {
		t.Errorf("third distinct trap label = %q, want other", got)
	}
	if got := pub.samples[0].Labels["trap"]; got != "1.3.6.1.4.1.9.0.1" {
		t.Errorf("first label = %q", got)
	}
}

func TestMalformedDatagramIsCounted(t *testing.T) {
	l, _, _ := newListener()
	l.Handle([]byte("PROBE"), udp("192.0.2.10"), t0)
	if l.stats.malformed.Load() != 1 || len(drain(l)) != 0 {
		t.Errorf("malformed=%d", l.stats.malformed.Load())
	}
}

func TestParseAllow(t *testing.T) {
	p, err := ParseAllow("10.0.0.0/8, 192.0.2.5 ,")
	if err != nil || len(p) != 2 || p[1] != netip.MustParsePrefix("192.0.2.5/32") {
		t.Errorf("ParseAllow = %v, %v", p, err)
	}
	if _, err := ParseAllow("nope"); err == nil {
		t.Error("garbage accepted")
	}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Vendor names come from the generated table; a standard OID must still win,
// and an unlisted vendor OID stays numeric rather than borrowing a neighbour.
func TestTrapNameCoversVendorMIBs(t *testing.T) {
	for oid, want := range map[string]string{
		"1.3.6.1.6.3.1.1.5.3":             "linkDown",
		"1.3.6.1.4.1.12356.101.2.0.302":   "fgTrapVpnTunDown",
		"1.3.6.1.4.1.12356.101.2.0.401":   "fgTrapHaSwitch",
		"1.3.6.1.4.1.12356.100.1.3.0.101": "fnTrapCpuThreshold",
		"1.3.6.1.4.1.25461.2.1.3.2.0.4":   "panThreatTrap",
		"1.3.6.1.4.1.12356.101.2.0.300":   "",
	} {
		if got := TrapName(oid); got != want {
			t.Errorf("TrapName(%s) = %q, want %q", oid, got, want)
		}
	}
	for oid := range vendorTrapNames {
		if _, dup := trapNames[oid]; dup {
			t.Errorf("%s is in both the standard and vendor tables", oid)
		}
	}
}
