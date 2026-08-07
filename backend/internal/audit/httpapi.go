package audit

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

// Handler — audit viewer API (FR-AUD-03: Admin + Auditor only).
type Handler struct {
	Pool    *pgxpool.Pool
	Checker authz.Checker
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(httpx.RequirePerm(h.Checker, authz.AuditRead))
		g.Get("/audit-events", h.list)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	sql := `SELECT id, at, actor_kind, coalesce(actor_id,''), action,
		coalesce(resource_kind,''), coalesce(resource_id,''),
		coalesce(source_ip::text,''), coalesce(trace_id,''), detail
		FROM audit.events WHERE true`
	args := []any{}
	if v := q.Get("action_prefix"); v != "" {
		args = append(args, v+"%")
		sql += ` AND action LIKE $` + strconv.Itoa(len(args))
	}
	if v := q.Get("actor"); v != "" {
		args = append(args, v)
		sql += ` AND actor_id = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			args = append(args, t)
			sql += ` AND at >= $` + strconv.Itoa(len(args))
		}
	}
	args = append(args, limit)
	sql += ` ORDER BY at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := h.Pool.Query(r.Context(), sql, args...)
	if err != nil {
		httpx.WriteError(w, r, errx.Wrap(errx.KindTransient, err, "audit query"))
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var at time.Time
		var actorKind, actorID, action, resKind, resID, sourceIP, traceID string
		var detail map[string]any
		if err := rows.Scan(&id, &at, &actorKind, &actorID, &action, &resKind,
			&resID, &sourceIP, &traceID, &detail); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{
			"id": id, "at": at.UTC().Format(time.RFC3339),
			"actor_kind": actorKind, "actor_id": actorID, "action": action,
			"resource_kind": resKind, "resource_id": resID,
			"source_ip": sourceIP, "trace_id": traceID, "detail": detail,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}
