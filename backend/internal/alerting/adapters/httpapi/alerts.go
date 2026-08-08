// Package httpapi — alerts, alert-rule CRUD and silences routes (doc 09 §9).
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	alertpg "github.com/freezxp/netinv/backend/internal/alerting/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/alerting/app"
	"github.com/freezxp/netinv/backend/internal/alerting/domain"
	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

type Handler struct {
	Store   *alertpg.Store
	Pool    *pgxpool.Pool
	Checker authz.Checker
	// Audit records rule changes. Disabling a rule silences monitoring, which
	// is exactly the kind of act an operator needs to be able to trace later.
	Audit audit.Writer
	// Exprs validates rule expressions against the metrics backend before a
	// rule is stored. Optional; nil skips the check.
	Exprs app.ExprValidator
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequirePerm(h.Checker, authz.AlertsRead))
		pr.Get("/alerts", h.list)
		pr.Get("/alerts/{id}", h.get)
		pr.Get("/alert-rules", h.rules)
		pr.Get("/silences", h.silences)
	})
	r.Group(func(pa chi.Router) {
		pa.Use(httpx.RequirePerm(h.Checker, authz.AlertsAck))
		pa.Post("/alerts/{id}/ack", h.ack)
		pa.Post("/alerts/{id}/unack", h.unack)
		pa.Post("/silences", h.createSilence)
		pa.Delete("/silences/{id}", h.revokeSilence)
	})
	r.Group(func(pw chi.Router) {
		pw.Use(httpx.RequirePerm(h.Checker, authz.AlertsAdmin))
		pw.Post("/alert-rules", h.createRule)
		pw.Patch("/alert-rules/{id}", h.updateRule)
		pw.Delete("/alert-rules/{id}", h.deleteRule)
		pw.Post("/alert-rules/{id}/enable", h.setRuleEnabled(true))
		pw.Post("/alert-rules/{id}/disable", h.setRuleEnabled(false))
	})
}

type alertView struct {
	ID          string            `json:"id"`
	Rule        map[string]string `json:"rule"`
	State       string            `json:"state"`
	Severity    string            `json:"severity"`
	DeviceID    string            `json:"device_id,omitempty"`
	InterfaceID string            `json:"interface_id,omitempty"`
	Labels      map[string]string `json:"labels"`
	Value       float64           `json:"value"`
	FiredAt     string            `json:"fired_at"`
	DurationS   int64             `json:"duration_s"`
	Acked       map[string]string `json:"acked,omitempty"`
	ResolvedAt  *string           `json:"resolved_at,omitempty"`
	FlapCount   int               `json:"flap_count,omitempty"`
}

func toView(i *domain.Instance) alertView {
	v := alertView{
		ID: i.ID, Rule: map[string]string{"id": i.RuleID, "name": i.RuleName},
		State: string(i.State), Severity: string(i.Severity),
		DeviceID: i.DeviceID, InterfaceID: i.InterfaceID, Labels: i.Labels,
		Value: i.Value, FiredAt: i.FiredAt.UTC().Format(time.RFC3339),
		DurationS: int64(time.Since(i.FiredAt).Seconds()), FlapCount: i.FlapCount,
	}
	if i.AckedAt != nil {
		v.Acked = map[string]string{
			"by": i.AckedBy, "at": i.AckedAt.UTC().Format(time.RFC3339),
			"comment": i.AckComment,
		}
	}
	if i.ResolvedAt != nil {
		s := i.ResolvedAt.UTC().Format(time.RFC3339)
		v.ResolvedAt = &s
		v.DurationS = int64(i.ResolvedAt.Sub(i.FiredAt).Seconds())
	}
	return v
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	f := alertpg.ListFilter{}
	for part := range strings.SplitSeq(r.URL.Query().Get("filter"), ",") {
		bits := strings.SplitN(part, ":", 3)
		if len(bits) != 3 {
			continue
		}
		switch bits[0] + ":" + bits[1] {
		case "state:in":
			f.States = strings.Split(bits[2], "|")
		case "severity:in":
			f.Severity = strings.Split(bits[2], "|")
		case "device:eq":
			f.DeviceID = bits[2]
		}
	}
	items, err := h.Store.List(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]alertView, 0, len(items))
	for _, i := range items {
		out = append(out, toView(i))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	i, err := h.Store.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	events, _ := h.Store.Events(r.Context(), i.ID)
	v := toView(i)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"alert": v, "events": events})
}

func (h *Handler) ack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Comment string `json:"comment"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	userID := httpx.ClaimsFrom(r.Context()).Subject
	alertID := chi.URLParam(r, "id")
	if err := h.Store.Ack(r.Context(), alertID, userID, req.Comment); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.Store.AppendEvent(r.Context(), alertID, "acked", userID,
		map[string]any{"comment": req.Comment})
	i, err := h.Store.Get(r.Context(), alertID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toView(i))
}

func (h *Handler) unack(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "id")
	if err := h.Store.Unack(r.Context(), alertID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.Store.AppendEvent(r.Context(), alertID, "unacked",
		httpx.ClaimsFrom(r.Context()).Subject, nil)
	i, _ := h.Store.Get(r.Context(), alertID)
	httpx.WriteJSON(w, http.StatusOK, toView(i))
}

func (h *Handler) rules(w http.ResponseWriter, r *http.Request) {
	out, err := h.loadRules(r.Context(), "")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

// loadRules returns every rule, or just one when id is given. The live count
// comes along so the UI can warn before disabling a rule that is currently
// reporting something.
func (h *Handler) loadRules(ctx context.Context, id string) ([]*app.RuleSummary, error) {
	q := `
		SELECT r.id, r.name, r.kind, r.severity, coalesce(r.expr,''), r.condition,
		       r.annotations, r.enabled, r.is_builtin,
		       (SELECT count(*) FROM alerting.alert_instances ai
		        WHERE ai.rule_id = r.id AND ai.state != 'resolved')
		FROM alerting.alert_rules r`
	args := []any{}
	if id != "" {
		q += ` WHERE r.id = $1`
		args = append(args, id)
	}
	q += ` ORDER BY r.name`
	rows, err := h.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list alert rules")
	}
	defer rows.Close()
	out := []*app.RuleSummary{}
	for rows.Next() {
		v := &app.RuleSummary{}
		if err := rows.Scan(&v.ID, &v.Name, &v.Kind, &v.Severity, &v.Expr,
			&v.Condition, &v.Annotations, &v.Enabled, &v.Builtin, &v.Firing); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (h *Handler) oneRule(ctx context.Context, id string) (*app.RuleSummary, error) {
	rules, err := h.loadRules(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, errx.New(errx.KindNotFound, "rule not found")
	}
	return rules[0], nil
}

func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	var in app.RuleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	if err := in.Validate(r.Context(), h.Exprs, true); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	rid := id.New("ar")
	_, err := h.Pool.Exec(r.Context(), `
		INSERT INTO alerting.alert_rules
			(id, tenant_id, name, kind, severity, expr, condition, annotations, enabled)
		VALUES ($1,'t_default',$2,$3::alerting.rule_kind,$4::alerting.severity,
		        nullif($5,''),$6::jsonb,$7::jsonb,$8)`,
		rid, in.Name, in.Kind, in.Severity, in.Expr,
		jsonbOr(in.Condition, "{}"), jsonbOr(in.Annotations, "{}"), enabled)
	if err != nil {
		httpx.WriteError(w, r, errx.Wrap(errx.KindConflict, err,
			"could not create rule — the name may already be in use"))
		return
	}
	h.auditRule(r, "alert_rule.create", rid, nil, map[string]any{
		"name": in.Name, "severity": in.Severity, "expr": in.Expr, "enabled": enabled,
	})
	rule, err := h.oneRule(r.Context(), rid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, rule)
}

func (h *Handler) updateRule(w http.ResponseWriter, r *http.Request) {
	rid := chi.URLParam(r, "id")
	before, err := h.oneRule(r.Context(), rid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var in app.RuleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	if err := in.Validate(r.Context(), h.Exprs, false); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// Every field is optional: coalesce keeps whatever the caller left out.
	// A built-in rule is tunable — thresholds and severity are exactly what an
	// operator needs to adjust — it simply cannot be deleted.
	_, err = h.Pool.Exec(r.Context(), `
		UPDATE alerting.alert_rules SET
			name       = coalesce(nullif($2,''), name),
			kind       = coalesce(nullif($3,'')::alerting.rule_kind, kind),
			severity   = coalesce(nullif($4,'')::alerting.severity, severity),
			expr       = coalesce(nullif($5,''), expr),
			condition  = coalesce($6::jsonb, condition),
			annotations= coalesce($7::jsonb, annotations),
			enabled    = coalesce($8, enabled),
			updated_at = now()
		WHERE id = $1`,
		rid, in.Name, in.Kind, in.Severity, in.Expr,
		jsonbOr(in.Condition, ""), jsonbOr(in.Annotations, ""), in.Enabled)
	if err != nil {
		httpx.WriteError(w, r, errx.Wrap(errx.KindConflict, err,
			"could not update rule — the name may already be in use"))
		return
	}
	after, err := h.oneRule(r.Context(), rid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.auditRule(r, "alert_rule.update", rid,
		map[string]any{"name": before.Name, "severity": before.Severity,
			"expr": before.Expr, "enabled": before.Enabled},
		map[string]any{"name": after.Name, "severity": after.Severity,
			"expr": after.Expr, "enabled": after.Enabled})
	httpx.WriteJSON(w, http.StatusOK, after)
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	rid := chi.URLParam(r, "id")
	rule, err := h.oneRule(r.Context(), rid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := app.DeleteGuard(rule); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// Takes the rule's alert history with it: instances reference the rule and
	// cannot be orphaned, so the confirmation upstream says how many go.
	if err := h.Store.DeleteRule(r.Context(), rid); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.auditRule(r, "alert_rule.delete", rid,
		map[string]any{"name": rule.Name, "severity": rule.Severity,
			"expr": rule.Expr, "firing": rule.Firing}, nil)
	w.WriteHeader(http.StatusNoContent)
}

// jsonbOr renders a map for a ::jsonb parameter. Empty maps are ambiguous to
// the planner inside coalesce, so the caller states the fallback: "{}" to
// store an empty document, "" to mean "leave the stored value alone".
func jsonbOr(v map[string]any, fallback string) any {
	if v == nil {
		if fallback == "" {
			return nil
		}
		return fallback
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return string(raw)
}

func (h *Handler) auditRule(r *http.Request, action, rid string, before, after map[string]any) {
	if h.Audit == nil {
		return
	}
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i > 0 {
		ip = ip[:i]
	}
	h.Audit.Write(r.Context(), audit.Event{
		ActorKind: "user", ActorID: httpx.ClaimsFrom(r.Context()).Subject,
		Action: action, ResourceKind: "alert_rule", ResourceID: rid,
		Before: before, After: after,
		SourceIP: ip, UserAgent: r.UserAgent(), TraceID: httpx.TraceID(r.Context()),
	})
}

func (h *Handler) setRuleEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tag, err := h.Pool.Exec(r.Context(),
			`UPDATE alerting.alert_rules SET enabled=$2, updated_at=now() WHERE id=$1`,
			chi.URLParam(r, "id"), enabled)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			httpx.WriteError(w, r, errx.New(errx.KindNotFound, "rule not found"))
			return
		}
		h.auditRule(r, "alert_rule.set_enabled", chi.URLParam(r, "id"),
			map[string]any{"enabled": !enabled}, map[string]any{"enabled": enabled})
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) silences(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, scope, reason, starts_at, ends_at, created_by, revoked_at
		FROM alerting.silences ORDER BY ends_at DESC LIMIT 100`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var sid, reason, createdBy string
		var scope map[string]any
		var starts, ends time.Time
		var revoked *time.Time
		if err := rows.Scan(&sid, &scope, &reason, &starts, &ends, &createdBy, &revoked); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{
			"id": sid, "scope": scope, "reason": reason,
			"starts_at":  starts.UTC().Format(time.RFC3339),
			"ends_at":    ends.UTC().Format(time.RFC3339),
			"created_by": createdBy, "active": revoked == nil && time.Now().Before(ends),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) createSilence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope     map[string]string `json:"scope"`
		Reason    string            `json:"reason"`
		DurationS int               `json:"duration_s"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		len(req.Scope) == 0 || req.Reason == "" || req.DurationS <= 0 {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid,
			"scope, reason and duration_s are required"))
		return
	}
	sid := id.New("sil")
	scope, _ := json.Marshal(req.Scope)
	_, err := h.Pool.Exec(r.Context(), `
		INSERT INTO alerting.silences (id, tenant_id, scope, reason, starts_at, ends_at, created_by)
		VALUES ($1,'t_default',$2,$3,now(),now() + make_interval(secs => $4),$5)`,
		sid, scope, req.Reason, req.DurationS, httpx.ClaimsFrom(r.Context()).Subject)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"id": sid})
}

func (h *Handler) revokeSilence(w http.ResponseWriter, r *http.Request) {
	tag, err := h.Pool.Exec(r.Context(),
		`UPDATE alerting.silences SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`,
		chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, r, errx.New(errx.KindNotFound, "silence not found or already revoked"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
