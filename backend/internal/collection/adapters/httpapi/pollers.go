// Package httpapi — poller fleet routes (doc 09 §4). Register/heartbeat are
// authenticated by enrollment/poller tokens, not user JWTs.
package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	colpg "github.com/freezxp/netinv/backend/internal/collection/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/collection/app"
	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

type PollerHandler struct {
	Svc     *app.PollerService
	Checker authz.Checker
}

// RegisterPublic mounts the token-authenticated endpoints (no user JWT).
func (h *PollerHandler) RegisterPublic(r chi.Router) {
	r.Post("/pollers/register", h.register)
	r.Post("/pollers/{id}/heartbeat", h.heartbeat)
}

// RegisterAuthed mounts the fleet-management endpoints.
func (h *PollerHandler) RegisterAuthed(r chi.Router) {
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequirePerm(h.Checker, authz.PlatformRead))
		pr.Get("/pollers", h.list)
		pr.Get("/pollers/{id}", h.get)
		pr.Get("/connectors", h.connectors)
	})
	r.Group(func(pw chi.Router) {
		pw.Use(httpx.RequirePerm(h.Checker, authz.PlatformWrite))
		pw.Post("/pollers/enroll-tokens", h.issueToken)
		pw.Post("/pollers/{id}/approve", h.approve)
		pw.Post("/pollers/{id}/disable", h.disable)
	})
}

func (h *PollerHandler) meta(r *http.Request) app.PollerMeta {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return app.PollerMeta{
		Actor:     httpx.ClaimsFrom(r.Context()).Subject,
		SourceIP:  ip,
		UserAgent: r.UserAgent(),
		TraceID:   httpx.TraceID(r.Context()),
	}
}

type pollerView struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	SiteID          string         `json:"site_id"`
	Status          string         `json:"status"`
	Version         string         `json:"version,omitempty"`
	LastHeartbeatAt *string        `json:"last_heartbeat_at"`
	Stats           map[string]any `json:"stats"`
}

func toPollerView(p *domain.Poller) pollerView {
	var hb *string
	if p.LastHeartbeatAt != nil {
		s := p.LastHeartbeatAt.UTC().Format("2006-01-02T15:04:05Z")
		hb = &s
	}
	if p.Stats == nil {
		p.Stats = map[string]any{}
	}
	return pollerView{ID: p.ID, Name: p.Name, SiteID: p.SiteID,
		Status: string(p.Status), Version: p.Version, LastHeartbeatAt: hb, Stats: p.Stats}
}

func (h *PollerHandler) list(w http.ResponseWriter, r *http.Request) {
	pollers, err := h.Svc.Repo.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]pollerView, 0, len(pollers))
	for _, p := range pollers {
		out = append(out, toPollerView(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *PollerHandler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.Svc.Repo.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPollerView(p))
}

// connectors serves the catalog (FR-PLT-03).
func (h *PollerHandler) connectors(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Svc.Repo.(*colpg.PollerRepo).Pool.Query(r.Context(), `
		SELECT id, vendor, display_name, version, capabilities, enabled,
			(SELECT count(*) FROM inventory.devices d
			 WHERE d.connector_id = c.id AND d.status != 'retired')
		FROM platform.connectors c ORDER BY vendor`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, vendor, name, version string
		var caps []string
		var enabled bool
		var devices int
		if err := rows.Scan(&id, &vendor, &name, &version, &caps, &enabled, &devices); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{
			"id": id, "vendor": vendor, "display_name": name, "version": version,
			"capabilities": caps, "enabled": enabled, "device_count": devices,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *PollerHandler) issueToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		SiteID string `json:"site_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	p, token, err := h.Svc.IssueEnrollToken(r.Context(), req.Name, req.SiteID, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"poller_id": p.ID, "token": token, "expires_in_s": 900,
	})
}

func (h *PollerHandler) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token   string `json:"token"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "token is required"))
		return
	}
	res, err := h.Svc.Register(r.Context(), req.Token, req.Version, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
}

func (h *PollerHandler) heartbeat(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Poller-Token")
	var stats domain.HeartbeatStats
	var body struct {
		Version string                `json:"version"`
		Stats   domain.HeartbeatStats `json:"stats"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
		stats = body.Stats
	}
	if err := h.Svc.Heartbeat(r.Context(), chi.URLParam(r, "id"), token,
		body.Version, stats); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PollerHandler) approve(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.Approve(r.Context(), chi.URLParam(r, "id"), h.meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := h.Svc.Repo.Get(r.Context(), chi.URLParam(r, "id"))
	httpx.WriteJSON(w, http.StatusOK, toPollerView(p))
}

func (h *PollerHandler) disable(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.Disable(r.Context(), chi.URLParam(r, "id"), h.meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
