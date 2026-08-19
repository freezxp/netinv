// Package app — rule evaluation loop and alert lifecycle (doc 05 §2 alerter).
package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/alerting/domain"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

// MetricsReader runs an instant MetricsQL query (doc 17 §4).
type MetricsReader interface {
	Query(ctx context.Context, expr string) ([]Series, error)
}

type Series struct {
	Labels map[string]string
	Value  float64
}

type RuleRepo interface {
	EnabledRules(ctx context.Context) ([]*domain.Rule, error)
}

type InstanceRepo interface {
	LiveByRule(ctx context.Context, ruleID string) ([]*domain.Instance, error)
	// RecentResolved returns the newest resolved instance for a fingerprint
	// within the flap window, if any.
	RecentResolved(ctx context.Context, ruleID, fingerprint string, since time.Time) (*domain.Instance, error)
	Fire(ctx context.Context, inst *domain.Instance) error
	Resolve(ctx context.Context, instID string, at time.Time) error
	SetFlapping(ctx context.Context, instID string) error
	AppendEvent(ctx context.Context, instID, event, actorID string, detail map[string]any) error
}

// SilenceChecker reports whether an alert's labels fall under an active
// silence (notification suppression only — state still recorded, FR-ALR-06).
type SilenceChecker interface {
	Silenced(ctx context.Context, labels map[string]string) (bool, error)
}

// AlertPublisher emits alert.* events for the notifier (doc 05 §8).
type AlertPublisher interface {
	Publish(ctx context.Context, event string, inst *domain.Instance, rule *domain.Rule) error
}

// InterfaceInfo is what alert suppression needs to know about an interface.
type InterfaceInfo struct {
	Name string
	// Alias is ifAlias — the operator's own description of the port ("uplink to
	// core", a customer name). Where it is set it says what the port is *for*,
	// which is more use in an alert than either the ifIndex or the system name.
	Alias string
	// EverUp is false until sync has observed the interface operationally up.
	// Monotonic once true — see FR-ALR-08 for why this is a stored flag rather
	// than a lookback window.
	EverUp bool
}

// InterfaceResolver best-effort maps (device_id, if_index) → interface id, and
// exposes a device's interfaces for alert suppression.
type InterfaceResolver interface {
	InterfaceID(ctx context.Context, deviceID, ifIndex string) string
	// Interfaces returns if_index → info for one device. A nil map simply
	// disables suppression for that device.
	Interfaces(ctx context.Context, deviceID string) map[string]InterfaceInfo
}

// ExporterResolver maps a flow exporter address to the device that claims it.
//
// Flow series are keyed by the datagram's source address and carry no device
// labels (doc 34 §3.1). Every other alert family carries `device`, which is
// what lets an operator act on an alert without a lookup; this closes that gap.
type ExporterResolver interface {
	// DeviceByExporter returns the device id and name claiming an exporter
	// address, or empty strings when nothing claims it.
	DeviceByExporter(ctx context.Context, addr string) (id, name string)
}

type Evaluator struct {
	Rules     RuleRepo
	Instances InstanceRepo
	Metrics   MetricsReader
	Silences  SilenceChecker
	Publish   AlertPublisher
	Ifaces    InterfaceResolver
	Exporters ExporterResolver
	Log       *slog.Logger
	Now       func() time.Time
}

func (e *Evaluator) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

// Cycle runs one evaluation pass over all enabled rules (default cadence 30s,
// FR-ALR-04).
func (e *Evaluator) Cycle(ctx context.Context) {
	rules, err := e.Rules.EnabledRules(ctx)
	if err != nil {
		e.Log.Error("load rules failed", "err", err)
		return
	}
	for _, rule := range rules {
		if rule.Expr == "" {
			continue // event-driven kinds without exprs are inert until their sources exist
		}
		if err := e.evalRule(ctx, rule); err != nil {
			e.Log.Warn("rule evaluation failed", "rule", rule.ID, "err", err)
		}
	}
}

func (e *Evaluator) evalRule(ctx context.Context, rule *domain.Rule) error {
	series, err := e.Metrics.Query(ctx, rule.Expr)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	series, ifaces := e.suppressNoise(ctx, series)
	live, err := e.Instances.LiveByRule(ctx, rule.ID)
	if err != nil {
		return err
	}
	liveByFP := map[string]*domain.Instance{}
	for _, inst := range live {
		liveByFP[inst.Fingerprint] = inst
	}
	now := e.now()

	matched := map[string]bool{}
	for _, s := range series {
		fp := domain.Fingerprint(rule.ID, s.Labels)
		if matched[fp] {
			continue // duplicate series collapse
		}
		matched[fp] = true
		if _, alreadyLive := liveByFP[fp]; alreadyLive {
			continue // still firing — nothing to do
		}
		// New firing. Flap detection: recent resolve of the same series?
		flapCount := 0
		if prev, _ := e.Instances.RecentResolved(ctx, rule.ID, fp, now.Add(-domain.FlapWindow)); prev != nil {
			flapCount = prev.FlapCount + 1
		}
		inst := &domain.Instance{
			ID: id.New("al"), RuleID: rule.ID, RuleName: rule.Name,
			Fingerprint: fp, State: domain.StateFiring, Severity: rule.Severity,
			DeviceID: s.Labels["device_id"], Labels: s.Labels, Value: s.Value,
			FiredAt: now, FlapCount: flapCount,
		}
		// Everything below adds labels for readability only, and all of it runs
		// *after* fingerprinting, so alert identity stays metric identity:
		// renaming an interface, or claiming an exporter onto a device, must not
		// resolve the old alert and fire a new one in its place.
		enriched := false
		enrich := func(k, v string) {
			if v == "" {
				return
			}
			if !enriched {
				cp := make(map[string]string, len(s.Labels)+2)
				for lk, lv := range s.Labels {
					cp[lk] = lv
				}
				inst.Labels = cp
				enriched = true
			}
			inst.Labels[k] = v
		}
		if e.Ifaces != nil && s.Labels["if_index"] != "" && inst.DeviceID != "" {
			inst.InterfaceID = e.Ifaces.InterfaceID(ctx, inst.DeviceID, s.Labels["if_index"])
			// Annotations say "Interface {{if_name}} on {{device}}", but the
			// metric only carries if_index, so summaries read as a blank.
			info := ifaces[inst.DeviceID][s.Labels["if_index"]]
			enrich("if_name", info.Name)
			enrich("if_alias", info.Alias)
		}
		// Flow series carry an exporter address and no device labels at all, so a
		// flow alert can name an address and nothing else. Resolve it to the
		// device that claims it, so the alert names a host like every other
		// family and the UI can offer a graph link. `device` is set either way —
		// to the claiming device when there is one, otherwise to the address —
		// so a summary written around {{device}} still reads correctly when
		// nothing claims the exporter, which is itself worth seeing (doc 34 §3.1).
		if exporter := s.Labels["exporter"]; exporter != "" && inst.DeviceID == "" {
			name := exporter
			if e.Exporters != nil {
				if id, devName := e.Exporters.DeviceByExporter(ctx, exporter); id != "" {
					inst.DeviceID = id
					if devName != "" {
						name = devName
					}
				}
			}
			enrich("device", name)
		}
		if err := e.Instances.Fire(ctx, inst); err != nil {
			e.Log.Error("fire failed", "rule", rule.ID, "err", err)
			continue
		}
		event := "fired"
		if flapCount >= domain.FlapThreshold {
			if err := e.Instances.SetFlapping(ctx, inst.ID); err == nil {
				inst.State = domain.StateFlapping
				event = "flap_start"
			}
		}
		_ = e.Instances.AppendEvent(ctx, inst.ID, event, "", map[string]any{
			"value": s.Value, "flap_count": flapCount,
		})
		e.notify(ctx, "alert.fired", inst, rule)
	}

	// Anything live but no longer matching resolves (FR-ALR-03).
	for fp, inst := range liveByFP {
		if matched[fp] {
			continue
		}
		if err := e.Instances.Resolve(ctx, inst.ID, now); err != nil {
			e.Log.Error("resolve failed", "alert", inst.ID, "err", err)
			continue
		}
		_ = e.Instances.AppendEvent(ctx, inst.ID, "resolved", "", nil)
		inst.State = domain.StateResolved
		e.notify(ctx, "alert.resolved", inst, rule)
	}
	return nil
}

// parentInterface returns the interface a subinterface hangs off, by the
// dot convention every platform we collect from uses (eth9.101 → eth9,
// and Q-in-Q eth9.101.200 → eth9.101). Empty when the name is not a
// subinterface.
func parentInterface(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[:i]
	}
	return ""
}

// suppressNoise drops interface series that describe no actionable fault
// (FR-ALR-08). Two causes, both scoped per device and to a single rule's own
// results:
//
//   - a subinterface whose parent is firing the same rule. One unplugged port
//     takes its VLAN children down with it, and reporting each of them is
//     fifteen alerts for one fault. A subinterface down while its parent is
//     healthy still alerts — that is a genuine fault, not a consequence.
//   - an interface never observed in service. A port never plugged in reports
//     down forever, and "never worked" is not an incident. The flag behind it
//     is monotonic, so a port that has ever worked alerts indefinitely.
//
// Anything without an if_index — device-scoped rules like CPU or ICMP — passes
// straight through. Returns the surviving series plus the device → if_index →
// info lookup it built, which the caller reuses to label alerts readably.
func (e *Evaluator) suppressNoise(ctx context.Context, series []Series) ([]Series, map[string]map[string]InterfaceInfo) {
	if e.Ifaces == nil {
		return series, nil
	}
	ifaces := map[string]map[string]InterfaceInfo{} // device_id → if_index → info
	firing := map[string]map[string]bool{}          // device_id → name → firing
	for _, s := range series {
		dev, idx := s.Labels["device_id"], s.Labels["if_index"]
		if dev == "" || idx == "" {
			continue
		}
		if _, ok := ifaces[dev]; !ok {
			ifaces[dev] = e.Ifaces.Interfaces(ctx, dev)
			firing[dev] = map[string]bool{}
		}
		if n := ifaces[dev][idx].Name; n != "" {
			firing[dev][n] = true
		}
	}

	out := make([]Series, 0, len(series))
	var dependent, neverUp int
	for _, s := range series {
		dev, idx := s.Labels["device_id"], s.Labels["if_index"]
		info, known := ifaces[dev][idx]
		// Parentage is tested against the unfiltered firing set: with Q-in-Q
		// the direct parent may itself be suppressed, and the child has to go
		// with it rather than be promoted to the only visible alert.
		if info.Name != "" && firing[dev][parentInterface(info.Name)] {
			dependent++
			continue
		}
		// Only suppress on a flag we actually hold. An interface inventory
		// knows nothing about keeps alerting — silence is the wrong default
		// when the data is missing.
		if known && !info.EverUp {
			neverUp++
			continue
		}
		out = append(out, s)
	}
	if dependent+neverUp > 0 {
		e.Log.Debug("suppressed interface alerts", "dependent", dependent,
			"never_in_service", neverUp, "remaining", len(out))
	}
	return out, ifaces
}

// notify publishes unless silenced or collapsed by flapping (FR-ALR-05/06).
func (e *Evaluator) notify(ctx context.Context, event string, inst *domain.Instance, rule *domain.Rule) {
	if inst.State == domain.StateFlapping && event == "alert.fired" {
		return // flapping alerts stop notifying until stable
	}
	if e.Silences != nil {
		if silenced, err := e.Silences.Silenced(ctx, inst.Labels); err == nil && silenced {
			_ = e.Instances.AppendEvent(ctx, inst.ID, "silenced", "", nil)
			return
		}
	}
	if e.Publish != nil {
		if err := e.Publish.Publish(ctx, event, inst, rule); err != nil {
			e.Log.Error("alert publish failed", "alert", inst.ID, "err", err)
		}
	}
}

// Run is the leader-gated loop (doc 05 §9).
type Leader interface {
	TryAcquire(ctx context.Context) bool
}

func (e *Evaluator) Run(ctx context.Context, leader Leader, interval time.Duration) error {
	if interval == 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if leader.TryAcquire(ctx) {
				e.Cycle(ctx)
			}
		}
	}
}
