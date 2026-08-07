// Package httpapi — alerts/silences routes (doc 09 §9). Rule CRUD beyond
// enable/disable lands with the admin UI sprint.
package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	alertpg "github.com/freezxp/netinv/backend/internal/alerting/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/alerting/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

type Handler struct {
	Store   *alertpg.Store
	Pool    *pgxpool.Pool
	Checker authz.Checker
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
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, name, kind, severity, coalesce(expr,''), enabled, is_builtin, annotations
		FROM alerting.alert_rules ORDER BY name`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, name, kind, sev, expr string
		var enabled, builtin bool
		var ann map[string]any
		if err := rows.Scan(&id, &name, &kind, &sev, &expr, &enabled, &builtin, &ann); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{
			"id": id, "name": name, "kind": kind, "severity": sev, "expr": expr,
			"enabled": enabled, "is_builtin": builtin, "annotations": ann,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
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
	var out []map[string]any
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
			"starts_at": starts.UTC().Format(time.RFC3339),
			"ends_at":   ends.UTC().Format(time.RFC3339),
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
