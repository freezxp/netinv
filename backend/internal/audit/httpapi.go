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
	// LEFT JOIN, not INNER: audit rows outlive the accounts that made them, and
	// an event whose user has since been deleted must still be listed — with
	// the id it was written with, which is the only identity it has left.
	sql := `SELECT e.id, e.at, e.actor_kind, coalesce(e.actor_id,''), e.action,
		coalesce(e.resource_kind,''), coalesce(e.resource_id,''),
		coalesce(e.source_ip::text,''), coalesce(e.trace_id,''), e.detail,
		coalesce(u.username,''), coalesce(u.display_name,'')
		FROM audit.events e
		LEFT JOIN iam.users u ON u.id = e.actor_id
		WHERE true`
	args := []any{}
	if v := q.Get("action_prefix"); v != "" {
		args = append(args, v+"%")
		sql += ` AND e.action LIKE $` + strconv.Itoa(len(args))
	}
	if v := q.Get("actor"); v != "" {
		// Accepts a username as readily as an id: nobody filtering an audit
		// log has a ULID to hand.
		args = append(args, v)
		n := strconv.Itoa(len(args))
		sql += ` AND (e.actor_id = $` + n + ` OR u.username = $` + n + `)`
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			args = append(args, t)
			sql += ` AND e.at >= $` + strconv.Itoa(len(args))
		}
	}
	args = append(args, limit)
	sql += ` ORDER BY e.at DESC, e.id DESC LIMIT $` + strconv.Itoa(len(args))

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
		var username, displayName string
		var detail map[string]any
		if err := rows.Scan(&id, &at, &actorKind, &actorID, &action, &resKind,
			&resID, &sourceIP, &traceID, &detail, &username, &displayName); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{
			"id": id, "at": at.UTC().Format(time.RFC3339),
			"actor_kind": actorKind, "actor_id": actorID, "action": action,
			// Resolved from iam.users; empty when the account is gone or the
			// event had no actor at all (a failed login has no user yet).
			"actor": username, "actor_display": displayName,
			"resource_kind": resKind, "resource_id": resID,
			"source_ip": sourceIP, "trace_id": traceID, "detail": detail,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}
