// Package httpapi — Inventory HTTP routes: /sites, /credentials (doc 09 §4–5).
package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

type Handler struct {
	Sites   *app.SiteService
	Creds   *app.CredentialService
	Checker authz.Checker
}

// Register adds inventory routes to an already-authenticated router.
func (h *Handler) Register(r chi.Router) {
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequirePerm(h.Checker, authz.PlatformRead))
		pr.Get("/sites", h.listSites)
		pr.Get("/sites/{id}", h.getSite)
	})
	r.Group(func(pw chi.Router) {
		pw.Use(httpx.RequirePerm(h.Checker, authz.PlatformWrite))
		pw.Post("/sites", h.createSite)
		pw.Patch("/sites/{id}", h.updateSite)
		pw.Delete("/sites/{id}", h.deleteSite)
	})
	r.Group(func(cr chi.Router) {
		cr.Use(httpx.RequirePerm(h.Checker, authz.CredentialsRead))
		cr.Get("/credentials", h.listCreds)
		cr.Get("/credentials/{id}", h.getCred)
	})
	r.Group(func(cw chi.Router) {
		cw.Use(httpx.RequirePerm(h.Checker, authz.CredentialsWrite))
		cw.Post("/credentials", h.createCred)
		cw.Patch("/credentials/{id}", h.updateCred)
		cw.Delete("/credentials/{id}", h.deleteCred)
		cw.Post("/credentials/{id}/test", h.testCred)
	})
}

func (h *Handler) meta(r *http.Request) app.Meta {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return app.Meta{
		Actor:     httpx.ClaimsFrom(r.Context()).Subject,
		SourceIP:  ip,
		UserAgent: r.UserAgent(),
		TraceID:   httpx.TraceID(r.Context()),
	}
}

// ---- sites ----

type siteView struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	ParentSiteID *string `json:"parent_site_id"`
	Location     string  `json:"location"`
	Contact      string  `json:"contact"`
	Status       string  `json:"status"`
}

func toSiteView(s *domain.Site) siteView {
	return siteView{ID: s.ID, Name: s.Name, ParentSiteID: s.ParentSiteID,
		Location: s.Location, Contact: s.Contact, Status: s.Status}
}

func (h *Handler) listSites(w http.ResponseWriter, r *http.Request) {
	sites, err := h.Sites.Repo.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]siteView, 0, len(sites))
	for _, s := range sites {
		out = append(out, toSiteView(s))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nil})
}

func (h *Handler) getSite(w http.ResponseWriter, r *http.Request) {
	s, err := h.Sites.Repo.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toSiteView(s))
}

func (h *Handler) createSite(w http.ResponseWriter, r *http.Request) {
	var in app.SiteInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	s, err := h.Sites.Create(r.Context(), in, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/sites/"+s.ID)
	httpx.WriteJSON(w, http.StatusCreated, toSiteView(s))
}

func (h *Handler) updateSite(w http.ResponseWriter, r *http.Request) {
	var in app.SiteInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	s, err := h.Sites.Update(r.Context(), chi.URLParam(r, "id"), in, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toSiteView(s))
}

func (h *Handler) deleteSite(w http.ResponseWriter, r *http.Request) {
	if err := h.Sites.Delete(r.Context(), chi.URLParam(r, "id"), h.meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- credentials ----

type credView struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Meta        map[string]any `json:"meta"`
	DeviceCount int            `json:"device_count"`
	CreatedAt   string         `json:"created_at"`
}

func toCredView(c *domain.Credential) credView {
	return credView{ID: c.ID, Name: c.Name, Kind: string(c.Kind), Meta: c.Meta,
		DeviceCount: c.DeviceCount,
		CreatedAt:   c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")}
}

func (h *Handler) listCreds(w http.ResponseWriter, r *http.Request) {
	creds, err := h.Creds.Vault.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]credView, 0, len(creds))
	for _, c := range creds {
		out = append(out, toCredView(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nil})
}

func (h *Handler) getCred(w http.ResponseWriter, r *http.Request) {
	c, err := h.Creds.Vault.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toCredView(c))
}

type credRequest struct {
	Name   string        `json:"name"`
	Kind   string        `json:"kind"`
	Secret domain.Secret `json:"secret"`
}

func (h *Handler) createCred(w http.ResponseWriter, r *http.Request) {
	var req credRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	c, err := h.Creds.Create(r.Context(), req.Name,
		domain.CredentialKind(req.Kind), req.Secret, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/credentials/"+c.ID)
	httpx.WriteJSON(w, http.StatusCreated, toCredView(c))
}

func (h *Handler) updateCred(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   *string        `json:"name"`
		Secret *domain.Secret `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	cid := chi.URLParam(r, "id")
	m := h.meta(r)
	if req.Name != nil {
		if err := h.Creds.Vault.Rename(r.Context(), cid, *req.Name); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if req.Secret != nil {
		if err := h.Creds.UpdateSecret(r.Context(), cid, *req.Secret, m); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	c, err := h.Creds.Vault.Get(r.Context(), cid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toCredView(c))
}

func (h *Handler) deleteCred(w http.ResponseWriter, r *http.Request) {
	if err := h.Creds.Delete(r.Context(), chi.URLParam(r, "id"), h.meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testCred(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetIP string `json:"target_ip"`
		Port     int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetIP == "" {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "target_ip is required"))
		return
	}
	res, err := h.Creds.Test(r.Context(), chi.URLParam(r, "id"),
		req.TargetIP, req.Port, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
