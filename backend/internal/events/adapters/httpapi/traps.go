// Package httpapi — read side of the events context (doc 09, doc 36 §6).
package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/events/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

type Handler struct {
	Store   *postgres.Store
	Checker authz.Checker
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(pr chi.Router) {
		// Traps are reports from devices about themselves, so they are read
		// under the same permission as the device pages that show them.
		pr.Use(httpx.RequirePerm(h.Checker, authz.DevicesRead))
		pr.Get("/events/traps", h.list)
	})
}

// list serves GET /events/traps — newest first, keyset-paginated by id.
//
//	device_id  only traps resolved to this device
//	unknown=1  only traps from addresses no device claims
//	q          trap name substring, or numeric OID prefix
//	before     return rows with id below this (the previous page's last id)
//	limit      1-500, default 100
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	rows, err := h.Store.ListTraps(r.Context(), postgres.TrapFilter{
		DeviceID:    strings.TrimSpace(q.Get("device_id")),
		UnknownOnly: q.Get("unknown") == "1",
		Q:           strings.TrimPrefix(strings.TrimSpace(q.Get("q")), "."),
		Before:      before,
		Limit:       limit,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var next int64
	if len(rows) > 0 {
		next = rows[len(rows)-1].ID
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": rows, "next_before": next})
}
