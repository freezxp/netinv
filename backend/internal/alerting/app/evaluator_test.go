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

// fakeIfaces serves interface names from a fixture, keyed device → index → name.
type fakeIfaces struct {
	names map[string]map[string]string
	calls int
}

func (f *fakeIfaces) InterfaceID(context.Context, string, string) string { return "" }
func (f *fakeIfaces) InterfaceNames(_ context.Context, dev string) map[string]string {
	f.calls++
	return f.names[dev]
}

func ifSeries(dev string, idx int) Series {
	return Series{
		Labels: map[string]string{"device_id": dev, "if_index": strconv.Itoa(idx)},
		Value:  2,
	}
}

func newEval(f *fakeIfaces) *Evaluator {
	return &Evaluator{
		Ifaces: f,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func names(t *testing.T, s []Series, f *fakeIfaces) []string {
	t.Helper()
	var out []string
	for _, x := range s {
		out = append(out, f.names[x.Labels["device_id"]][x.Labels["if_index"]])
	}
	return out
}

// The case from the field: one unplugged port took thirteen VLAN
// subinterfaces down with it and reported fifteen alerts for one fault.
func TestSuppressesVLANChildrenOfADownParent(t *testing.T) {
	fx := map[string]string{"3": "eth9", "79": "lag0"}
	var series []Series
	series = append(series, ifSeries("d1", 3), ifSeries("d1", 79))
	for i, vlan := range []string{"101", "102", "103", "104", "111", "13", "2", "20", "3", "4", "5", "6", "7"} {
		idx := 38 + i
		fx[strconv.Itoa(idx)] = "eth9." + vlan
		series = append(series, ifSeries("d1", idx))
	}
	f := &fakeIfaces{names: map[string]map[string]string{"d1": fx}}

	got, _ := newEval(f).suppressDependents(context.Background(), series)

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
		t.Errorf("looked up names %d times for one device, want 1", f.calls)
	}
}

// A subinterface down while its parent is fine is a real fault, not a
// consequence — suppressing it would hide the thing worth paging on.
func TestKeepsChildWhenParentIsHealthy(t *testing.T) {
	f := &fakeIfaces{names: map[string]map[string]string{
		"d1": {"3": "eth9", "38": "eth9.101"},
	}}
	got, _ := newEval(f).suppressDependents(context.Background(), []Series{ifSeries("d1", 38)})
	if len(got) != 1 {
		t.Fatalf("child alert was dropped though eth9 is up: %v", names(t, got, f))
	}
}

// Q-in-Q: the direct parent is itself suppressed, and the grandchild has to go
// with it rather than being promoted to the only visible alert.
func TestSuppressesGrandchildOfADownParent(t *testing.T) {
	f := &fakeIfaces{names: map[string]map[string]string{
		"d1": {"3": "eth9", "38": "eth9.101", "39": "eth9.101.200"},
	}}
	got, _ := newEval(f).suppressDependents(context.Background(),
		[]Series{ifSeries("d1", 3), ifSeries("d1", 38), ifSeries("d1", 39)})
	if len(got) != 1 || names(t, got, f)[0] != "eth9" {
		t.Fatalf("got %v, want [eth9]", names(t, got, f))
	}
}

// The same name on another device must not suppress anything here.
func TestSuppressionIsPerDevice(t *testing.T) {
	f := &fakeIfaces{names: map[string]map[string]string{
		"d1": {"3": "eth9"},
		"d2": {"38": "eth9.101"},
	}}
	got, _ := newEval(f).suppressDependents(context.Background(),
		[]Series{ifSeries("d1", 3), ifSeries("d2", 38)})
	if len(got) != 2 {
		t.Fatalf("cross-device suppression: got %v, want both", names(t, got, f))
	}
}

// Device-scoped rules (CPU, memory, ICMP) carry no if_index and must pass
// through untouched — including when no resolver is wired at all.
func TestNonInterfaceSeriesPassThrough(t *testing.T) {
	f := &fakeIfaces{}
	in := []Series{{Labels: map[string]string{"device_id": "d1"}, Value: 91}}
	if got, _ := newEval(f).suppressDependents(context.Background(), in); len(got) != 1 {
		t.Errorf("device-scoped series was suppressed")
	}
	if f.calls != 0 {
		t.Errorf("looked up interface names for a device-scoped rule")
	}
	bare := &Evaluator{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if got, _ := bare.suppressDependents(context.Background(), in); len(got) != 1 {
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

// ---- end to end through evalRule: one fault must fire exactly one alert ----

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

func TestEvalRuleFiresOnceForOneFault(t *testing.T) {
	f := &fakeIfaces{names: map[string]map[string]string{
		"d1": {"3": "eth9", "38": "eth9.101", "39": "eth9.102"},
	}}
	inst := &stubInstances{}
	e := &Evaluator{
		Instances: inst,
		Metrics:   stubMetrics{series: []Series{ifSeries("d1", 3), ifSeries("d1", 38), ifSeries("d1", 39)}},
		Ifaces:    f,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := e.evalRule(context.Background(),
		&domain.Rule{ID: "ar_if_down", Name: "Interface down", Expr: "x"}); err != nil {
		t.Fatal(err)
	}
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

// Alert identity must stay metric identity: adding the readable name to the
// labels must not change the fingerprint, or every alert re-fires on upgrade
// and again on any interface rename.
func TestNameLabelDoesNotChangeFingerprint(t *testing.T) {
	metric := map[string]string{"device_id": "d1", "if_index": "3"}
	withName := map[string]string{"device_id": "d1", "if_index": "3", "if_name": "eth9"}
	if domain.Fingerprint("ar_if_down", metric) == domain.Fingerprint("ar_if_down", withName) {
		t.Skip("fingerprint ignores extra labels; nothing to guard")
	}

	f := &fakeIfaces{names: map[string]map[string]string{"d1": {"3": "eth9"}}}
	inst := &stubInstances{}
	e := &Evaluator{
		Instances: inst, Metrics: stubMetrics{series: []Series{ifSeries("d1", 3)}},
		Ifaces: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := e.evalRule(context.Background(),
		&domain.Rule{ID: "ar_if_down", Name: "Interface down", Expr: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := inst.fired[0].Fingerprint; got != domain.Fingerprint("ar_if_down", metric) {
		t.Errorf("fingerprint changed once if_name was added: %s", got)
	}
}
