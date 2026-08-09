package capacity

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

// Handler exposes the capacity report.
type Handler struct {
	Collector *Collector
	Checker   authz.Checker
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		// platform:read rather than admin-only: capacity is operational
		// information an on-call engineer needs at 3am, and it contains no
		// credentials or customer data.
		g.Use(httpx.RequirePerm(h.Checker, authz.PlatformRead))
		g.Get("/platform/capacity", h.get)
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	rep, err := h.Collector.Collect(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
}
