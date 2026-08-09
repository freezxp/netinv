package polling

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

type Handler struct {
	Store   *Store
	Checker authz.Checker
	Audit   audit.Writer
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(httpx.RequirePerm(h.Checker, authz.PlatformRead))
		g.Get("/platform/polling", h.get)
	})
	r.Group(func(g chi.Router) {
		// Changing the fleet's cadence alters load on every monitored device,
		// so it needs the write permission rather than the read one.
		g.Use(httpx.RequirePerm(h.Checker, authz.PlatformWrite))
		g.Put("/platform/polling", h.put)
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	s, err := h.Store.Get(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}

// sourceIP strips the port from RemoteAddr. The audit column is `inet`, which
// rejects "10.0.0.1:54321" outright — and because audit failures are logged
// rather than returned, passing RemoteAddr straight through meant the change
// succeeded while its record silently did not.
func sourceIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return ip
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TrafficIntervalS int `json:"traffic_interval_s"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "invalid JSON body"))
		return
	}
	before, _ := h.Store.Get(r.Context())
	after, err := h.Store.Set(r.Context(), in.TrafficIntervalS)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if h.Audit != nil {
		// Worth auditing: a fleet-wide cadence change explains both a step in
		// storage growth and a change in graph resolution, and someone reading
		// those later should not have to guess why.
		h.Audit.Write(r.Context(), audit.Event{
			ActorKind:    "user",
			ActorID:      httpx.ClaimsFrom(r.Context()).Subject,
			Action:       "platform.polling.update",
			ResourceKind: "polling_profile",
			ResourceID:   "default",
			Before:       before,
			After:        after,
			SourceIP:     sourceIP(r),
			UserAgent:    r.UserAgent(),
			TraceID:      httpx.TraceID(r.Context()),
			Detail: map[string]any{
				"traffic_interval": Describe(after.TrafficIntervalS),
				"devices":          after.Devices,
			},
		})
	}
	httpx.WriteJSON(w, http.StatusOK, after)
}
