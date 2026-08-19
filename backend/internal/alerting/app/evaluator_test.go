package app

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/alerting/domain"
)

// fakeIfaces serves interfaces from a fixture, keyed device → if_index.
type fakeIfaces struct {
	ifaces map[string]map[string]InterfaceInfo
	calls  int
}

func (f *fakeIfaces) InterfaceID(context.Context, string, string) string { return "" }
func (f *fakeIfaces) Interfaces(_ context.Context, dev string) map[string]InterfaceInfo {
	f.calls++
	return f.ifaces[dev]
}

// up builds a fixture entry for an interface that has been in service, which
// is the ordinary case — never-up is the exception these tests call out.
func up(name string) InterfaceInfo { return InterfaceInfo{Name: name, EverUp: true} }

func ifSeries(dev string, idx int) Series {
	return Series{
		Labels: map[string]string{"device_id": dev, "if_index": strconv.Itoa(idx)},
		Value:  2,
	}
}

func newEval(f *fakeIfaces) *Evaluator {
	return &Evaluator{Ifaces: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func names(t *testing.T, s []Series, f *fakeIfaces) []string {
	t.Helper()
	var out []string
	for _, x := range s {
		out = append(out, f.ifaces[x.Labels["device_id"]][x.Labels["if_index"]].Name)
	}
	return out
}

// ---- dependent (parent/child) suppression ----

// The case from the field: one unplugged port took thirteen VLAN
// subinterfaces down with it and reported fifteen alerts for one fault.
func TestSuppressesVLANChildrenOfADownParent(t *testing.T) {
	fx := map[string]InterfaceInfo{"3": up("eth9"), "79": up("lag0")}
	series := []Series{ifSeries("d1", 3), ifSeries("d1", 79)}
	for i, vlan := range []string{"101", "102", "103", "104", "111", "13", "2", "20", "3", "4", "5", "6", "7"} {
		idx := 38 + i
		fx[strconv.Itoa(idx)] = up("eth9." + vlan)
		series = append(series, ifSeries("d1", idx))
	}
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{"d1": fx}}

	got, _ := newEval(f).suppressNoise(context.Background(), series)

	want := []string{"eth9", "lag0"} // lag0 is nobody's child, so it stands
	if len(got) != len(want) {
		t.Fatalf("got %d series %v, want %v", len(got), names(t, got, f), want)
	}
	for i, n := range names(t, got, f) {
		if n != want[i] {
			t.Errorf("series %d = %q, want %q", i, n, want[i])
		}
	}
	if f.calls != 1 {
		t.Errorf("looked up interfaces %d times for one device, want 1", f.calls)
	}
}

// A subinterface down while its parent is fine is a real fault, not a
// consequence — suppressing it would hide the thing worth paging on.
func TestKeepsChildWhenParentIsHealthy(t *testing.T) {
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{
		"d1": {"3": up("eth9"), "38": up("eth9.101")},
	}}
	got, _ := newEval(f).suppressNoise(context.Background(), []Series{ifSeries("d1", 38)})
	if len(got) != 1 {
		t.Fatalf("child alert was dropped though eth9 is up: %v", names(t, got, f))
	}
}

// Q-in-Q: the direct parent is itself suppressed, and the grandchild has to go
// with it rather than being promoted to the only visible alert.
func TestSuppressesGrandchildOfADownParent(t *testing.T) {
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{
		"d1": {"3": up("eth9"), "38": up("eth9.101"), "39": up("eth9.101.200")},
	}}
	got, _ := newEval(f).suppressNoise(context.Background(),
		[]Series{ifSeries("d1", 3), ifSeries("d1", 38), ifSeries("d1", 39)})
	if len(got) != 1 || names(t, got, f)[0] != "eth9" {
		t.Fatalf("got %v, want [eth9]", names(t, got, f))
	}
}

// The same name on another device must not suppress anything here.
func TestSuppressionIsPerDevice(t *testing.T) {
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{
		"d1": {"3": up("eth9")},
		"d2": {"38": up("eth9.101")},
	}}
	got, _ := newEval(f).suppressNoise(context.Background(),
		[]Series{ifSeries("d1", 3), ifSeries("d2", 38)})
	if len(got) != 2 {
		t.Fatalf("cross-device suppression: got %v, want both", names(t, got, f))
	}
}

// ---- never-in-service suppression ----

// A port never plugged in reports down forever. "Never worked" is not an
// incident, and it must not alert.
func TestSuppressesInterfaceNeverInService(t *testing.T) {
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{
		"d1": {"4": {Name: "eth1", EverUp: false}},
	}}
	got, _ := newEval(f).suppressNoise(context.Background(), []Series{ifSeries("d1", 4)})
	if len(got) != 0 {
		t.Fatalf("never-up port alerted: %v", names(t, got, f))
	}
}

// The whole point of a stored flag over a lookback window: a port that has
// once worked keeps alerting however long it stays down.
func TestKeepsAlertingOnAPortThatHasWorked(t *testing.T) {
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{
		"d1": {"4": up("eth1")},
	}}
	got, _ := newEval(f).suppressNoise(context.Background(), []Series{ifSeries("d1", 4)})
	if len(got) != 1 {
		t.Fatal("a port that has been in service stopped alerting")
	}
}

// Silence is the wrong default when inventory has no record of the interface:
// an unknown interface must still alert rather than be treated as never-up.
func TestUnknownInterfaceStillAlerts(t *testing.T) {
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{"d1": {}}}
	got, _ := newEval(f).suppressNoise(context.Background(), []Series{ifSeries("d1", 99)})
	if len(got) != 1 {
		t.Fatal("interface missing from inventory was silently suppressed")
	}
}

// Device-scoped rules (CPU, memory, ICMP) carry no if_index and must pass
// through untouched — including when no resolver is wired at all.
func TestNonInterfaceSeriesPassThrough(t *testing.T) {
	f := &fakeIfaces{}
	in := []Series{{Labels: map[string]string{"device_id": "d1"}, Value: 91}}
	if got, _ := newEval(f).suppressNoise(context.Background(), in); len(got) != 1 {
		t.Errorf("device-scoped series was suppressed")
	}
	if f.calls != 0 {
		t.Errorf("looked up interfaces for a device-scoped rule")
	}
	bare := &Evaluator{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if got, _ := bare.suppressNoise(context.Background(), in); len(got) != 1 {
		t.Errorf("nil resolver should pass series through")
	}
}

func TestParentInterface(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"eth9.101", "eth9"},
		{"eth9.101.200", "eth9.101"},
		{"eth9", ""},
		{"lag0", ""},
		{"", ""},
		{".5", ""}, // no parent name to speak of
	} {
		if got := parentInterface(tc.in); got != tc.want {
			t.Errorf("parentInterface(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---- end to end through evalRule ----

type stubInstances struct{ fired []*domain.Instance }

func (s *stubInstances) LiveByRule(context.Context, string) ([]*domain.Instance, error) {
	return nil, nil
}
func (s *stubInstances) RecentResolved(context.Context, string, string, time.Time) (*domain.Instance, error) {
	return nil, nil
}
func (s *stubInstances) Fire(_ context.Context, i *domain.Instance) error {
	s.fired = append(s.fired, i)
	return nil
}
func (s *stubInstances) Resolve(context.Context, string, time.Time) error { return nil }
func (s *stubInstances) SetFlapping(context.Context, string) error        { return nil }
func (s *stubInstances) AppendEvent(context.Context, string, string, string, map[string]any) error {
	return nil
}

type stubMetrics struct{ series []Series }

func (s stubMetrics) Query(context.Context, string) ([]Series, error) { return s.series, nil }

func run(t *testing.T, f *fakeIfaces, series []Series) *stubInstances {
	t.Helper()
	inst := &stubInstances{}
	e := &Evaluator{
		Instances: inst, Metrics: stubMetrics{series: series}, Ifaces: f,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := e.evalRule(context.Background(),
		&domain.Rule{ID: "ar_if_down", Name: "Interface down", Expr: "x"}); err != nil {
		t.Fatal(err)
	}
	return inst
}

func TestEvalRuleFiresOnceForOneFault(t *testing.T) {
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{
		"d1": {"3": up("eth9"), "38": up("eth9.101"), "39": up("eth9.102")},
	}}
	inst := run(t, f, []Series{ifSeries("d1", 3), ifSeries("d1", 38), ifSeries("d1", 39)})
	if len(inst.fired) != 1 {
		t.Fatalf("fired %d alerts for one down port, want 1", len(inst.fired))
	}
	if inst.fired[0].Labels["if_index"] != "3" {
		t.Errorf("fired on if_index %s, want the parent (3)", inst.fired[0].Labels["if_index"])
	}
	// Annotations render {{if_name}}; without this the summary reads
	// "Interface  on ...".
	if got := inst.fired[0].Labels["if_name"]; got != "eth9" {
		t.Errorf("alert if_name = %q, want %q", got, "eth9")
	}
}

// The fleet shape that prompted this: unused ports plus a parent's VLAN
// children, with one port that genuinely failed.
func TestEvalRuleReportsOnlyTheRealFault(t *testing.T) {
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{
		"d1": {
			"3":  {Name: "eth9", EverUp: false},  // never plugged in
			"5":  {Name: "eth10", EverUp: false}, // never plugged in
			"38": {Name: "eth9.101", EverUp: false},
			"7":  up("eth0"), // was working, now down — the actual incident
		},
	}}
	inst := run(t, f, []Series{ifSeries("d1", 3), ifSeries("d1", 5), ifSeries("d1", 38), ifSeries("d1", 7)})
	if len(inst.fired) != 1 {
		t.Fatalf("fired %d alerts, want only the port that failed", len(inst.fired))
	}
	if got := inst.fired[0].Labels["if_name"]; got != "eth0" {
		t.Errorf("alerted on %q, want eth0", got)
	}
}

// Alert identity must stay metric identity: adding the readable name to the
// labels must not change the fingerprint, or every alert re-fires on upgrade
// and again on any interface rename.
func TestNameLabelDoesNotChangeFingerprint(t *testing.T) {
	metric := map[string]string{"device_id": "d1", "if_index": "3"}
	withName := map[string]string{"device_id": "d1", "if_index": "3", "if_name": "eth9"}
	if domain.Fingerprint("ar_if_down", metric) == domain.Fingerprint("ar_if_down", withName) {
		t.Skip("fingerprint ignores extra labels; nothing to guard")
	}
	f := &fakeIfaces{ifaces: map[string]map[string]InterfaceInfo{"d1": {"3": up("eth9")}}}
	inst := run(t, f, []Series{ifSeries("d1", 3)})
	if got := inst.fired[0].Fingerprint; got != domain.Fingerprint("ar_if_down", metric) {
		t.Errorf("fingerprint changed once if_name was added: %s", got)
	}
}

// stubExporters resolves a fixed set of exporter addresses to devices.
type stubExporters struct{ byAddr map[string][2]string }

func (s stubExporters) DeviceByExporter(_ context.Context, addr string) (string, string) {
	if v, ok := s.byAddr[addr]; ok {
		return v[0], v[1]
	}
	return "", ""
}

func runFlow(t *testing.T, ex ExporterResolver, series []Series) *stubInstances {
	t.Helper()
	inst := &stubInstances{}
	e := &Evaluator{
		Instances: inst, Metrics: stubMetrics{series: series}, Exporters: ex,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := e.evalRule(context.Background(), &domain.Rule{
		ID: "ar_flow_exporter_stale", Name: "Flow exporter stopped exporting", Expr: "x",
	}); err != nil {
		t.Fatal(err)
	}
	return inst
}

func flowSeries(exporter string) Series {
	return Series{Labels: map[string]string{"exporter": exporter}, Value: 1}
}

// A flow alert must name a host. Flow series carry only the exporter address
// (doc 34 §3.1), so without resolution the summary reads "No flow from
// 192.0.2.7" — an address, to whoever is on call, with no graph link because
// the instance carries no device_id either.
func TestFlowAlertNamesTheDeviceBehindTheExporter(t *testing.T) {
	ex := stubExporters{byAddr: map[string][2]string{
		"192.0.2.7": {"d_fn", "FN gw"},
	}}
	inst := runFlow(t, ex, []Series{flowSeries("192.0.2.7")})
	if len(inst.fired) != 1 {
		t.Fatalf("fired %d, want 1", len(inst.fired))
	}
	got := inst.fired[0]
	if got.Labels["device"] != "FN gw" {
		t.Errorf("device label = %q, want %q — the summary renders {{device}}",
			got.Labels["device"], "FN gw")
	}
	if got.DeviceID != "d_fn" {
		t.Errorf("DeviceID = %q, want d_fn — without it the alert list shows no graph link",
			got.DeviceID)
	}
	if got.Labels["exporter"] != "192.0.2.7" {
		t.Errorf("exporter label lost: %q", got.Labels["exporter"])
	}
}

// An exporter nothing claims still has to produce a readable summary, so
// `device` falls back to the address rather than being left empty — an empty
// {{device}} renders as a blank and reads like a bug in the alert.
func TestFlowAlertFallsBackToAddressWhenUnclaimed(t *testing.T) {
	inst := runFlow(t, stubExporters{}, []Series{flowSeries("192.0.2.9")})
	if len(inst.fired) != 1 {
		t.Fatalf("fired %d, want 1", len(inst.fired))
	}
	if got := inst.fired[0].Labels["device"]; got != "192.0.2.9" {
		t.Errorf("device label = %q, want the address as a fallback", got)
	}
	if got := inst.fired[0].DeviceID; got != "" {
		t.Errorf("DeviceID = %q, want empty when nothing claims the exporter", got)
	}
}

// Resolution must not change alert identity. Claiming an exporter onto a device
// while an alert is live would otherwise resolve it and fire a duplicate — the
// same reason if_name is added after fingerprinting.
func TestFlowAlertFingerprintIgnoresResolvedDevice(t *testing.T) {
	unclaimed := runFlow(t, stubExporters{}, []Series{flowSeries("192.0.2.7")})
	claimed := runFlow(t, stubExporters{byAddr: map[string][2]string{
		"192.0.2.7": {"d_fn", "FN gw"},
	}}, []Series{flowSeries("192.0.2.7")})
	if unclaimed.fired[0].Fingerprint != claimed.fired[0].Fingerprint {
		t.Errorf("fingerprint changed when the exporter was claimed (%s vs %s); "+
			"the live alert would resolve and re-fire as a duplicate",
			unclaimed.fired[0].Fingerprint, claimed.fired[0].Fingerprint)
	}
}
