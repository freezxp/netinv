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

// InterfaceResolver best-effort maps (device_id, if_index) → interface id, and
// exposes a device's interface names for dependency suppression.
type InterfaceResolver interface {
	InterfaceID(ctx context.Context, deviceID, ifIndex string) string
	// InterfaceNames returns if_index → name for one device. A nil map simply
	// disables suppression for that device.
	InterfaceNames(ctx context.Context, deviceID string) map[string]string
}

type Evaluator struct {
	Rules     RuleRepo
	Instances InstanceRepo
	Metrics   MetricsReader
	Silences  SilenceChecker
	Publish   AlertPublisher
	Ifaces    InterfaceResolver
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
	series, ifNames := e.suppressDependents(ctx, series)
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
		if e.Ifaces != nil && s.Labels["if_index"] != "" && inst.DeviceID != "" {
			inst.InterfaceID = e.Ifaces.InterfaceID(ctx, inst.DeviceID, s.Labels["if_index"])
			// Annotations say "Interface {{if_name}} on {{device}}", but the
			// metric only carries if_index, so summaries read as a blank. Added
			// after fingerprinting so alert identity stays metric identity and
			// a rename doesn't re-fire the alert.
			if name := ifNames[inst.DeviceID][s.Labels["if_index"]]; name != "" {
				labels := make(map[string]string, len(s.Labels)+1)
				for k, v := range s.Labels {
					labels[k] = v
				}
				labels["if_name"] = name
				inst.Labels = labels
			}
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

// suppressDependents drops symptom series whose cause is already firing in the
// same result: one unplugged port takes its VLAN subinterfaces down with it,
// and reporting each of them is fifteen alerts for one fault.
//
// Only ever suppresses within a single rule's own result set, so a
// subinterface still alerts whenever its parent is healthy — that case is a
// genuine fault rather than a consequence. Interface-scoped rules are the ones
// this can apply to; anything without an if_index passes straight through.
// Returns the surviving series plus the device → if_index → name lookup it
// built, which the caller reuses to label alerts with a readable name.
func (e *Evaluator) suppressDependents(ctx context.Context, series []Series) ([]Series, map[string]map[string]string) {
	if e.Ifaces == nil {
		return series, nil
	}
	// Names of the interfaces this rule is firing on, per device.
	names := map[string]map[string]string{} // device_id → if_index → name
	firing := map[string]map[string]bool{}  // device_id → name → firing
	for _, s := range series {
		dev, idx := s.Labels["device_id"], s.Labels["if_index"]
		if dev == "" || idx == "" {
			continue
		}
		if _, ok := names[dev]; !ok {
			names[dev] = e.Ifaces.InterfaceNames(ctx, dev)
			firing[dev] = map[string]bool{}
		}
		if n := names[dev][idx]; n != "" {
			firing[dev][n] = true
		}
	}

	out := make([]Series, 0, len(series))
	suppressed := 0
	for _, s := range series {
		dev, idx := s.Labels["device_id"], s.Labels["if_index"]
		name := names[dev][idx]
		// Test against the unfiltered set: with Q-in-Q the direct parent may
		// itself be suppressed, and the child must still go with it.
		if name != "" && firing[dev][parentInterface(name)] {
			suppressed++
			continue
		}
		out = append(out, s)
	}
	if suppressed > 0 {
		e.Log.Debug("suppressed dependent interface alerts",
			"count", suppressed, "remaining", len(out))
	}
	return out, names
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
