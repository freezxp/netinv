package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/collection/app"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

// DiscoveryHandler — subnet discovery routes (doc 09 §16).
type DiscoveryHandler struct {
	Svc     *app.DiscoveryService
	Checker authz.Checker
	// IdentityMatch reports a managed device already reporting the identity a
	// find claims, so a device reachable on two addresses is not onboarded
	// twice. Wired in cmd; nil disables the check rather than failing closed —
	// a duplicate is a nuisance, being unable to onboard anything is an outage.
	IdentityMatch func(ctx context.Context, sysName, serial string) (id, name, ip, match string, err error)
	// AttachAddress records the find's address on the existing device instead
	// of creating a second record for it.
	AttachAddress func(ctx context.Context, deviceID, addr string) error
	// Onboard creates a managed device from an approved find; wired in cmd to
	// the inventory service (cross-context via function, doc 13 rule 3).
	Onboard func(ctx context.Context, in OnboardInput) (deviceID string, err error)
}

type OnboardInput struct {
	Name         string
	MgmtIP       string
	SiteID       string
	CredentialID string
	ConnectorID  string
}

func (h *DiscoveryHandler) Register(r chi.Router) {
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequirePerm(h.Checker, authz.PlatformRead))
		pr.Get("/discovery/rules", h.listRules)
		pr.Get("/discovery/found", h.listFound)
	})
	r.Group(func(pw chi.Router) {
		pw.Use(httpx.RequirePerm(h.Checker, authz.PlatformWrite))
		pw.Post("/discovery/rules", h.createRule)
		pw.Delete("/discovery/rules/{id}", h.deleteRule)
		pw.Post("/discovery/rules/{id}/run", h.run)
		pw.Post("/discovery/found/{id}/approve", h.approve)
		pw.Post("/discovery/found/{id}/ignore", h.ignore)
	})
}

func (h *DiscoveryHandler) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.Svc.Repo.ListRules(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": rules})
}

func (h *DiscoveryHandler) createRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SiteID        string   `json:"site_id"`
		CIDR          string   `json:"cidr"`
		CredentialIDs []string `json:"credential_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	rule, err := h.Svc.CreateRule(r.Context(), req.SiteID, req.CIDR,
		req.CredentialIDs, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, rule)
}

func (h *DiscoveryHandler) deleteRule(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.Repo.DeleteRule(r.Context(), chi.URLParam(r, "id")); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DiscoveryHandler) run(w http.ResponseWriter, r *http.Request) {
	jobID, err := h.Svc.Run(r.Context(), chi.URLParam(r, "id"), meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

func (h *DiscoveryHandler) listFound(w http.ResponseWriter, r *http.Request) {
	found, err := h.Svc.Repo.ListFound(r.Context(), r.URL.Query().Get("state"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": found})
}

// approve promotes a find into a managed device (FR-SYNC-04: nothing is
// auto-managed; this is the operator's explicit act).
func (h *DiscoveryHandler) approve(w http.ResponseWriter, r *http.Request) {
	if h.Onboard == nil {
		httpx.WriteError(w, r, errx.New(errx.KindTransient, "onboarding unavailable"))
		return
	}
	var req struct {
		Name   string `json:"name"`
		SiteID string `json:"site_id"`
		// Force onboards anyway when the identity check objects. Two devices
		// can legitimately share a hostname, so the check informs a decision
		// rather than making it.
		Force bool `json:"force"`
		// AttachTo records this address on an existing device instead of
		// creating a new one — the answer when the check is right.
		AttachTo string `json:"attach_to"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	found, err := h.Svc.Repo.GetFound(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if found.Managed {
		httpx.WriteError(w, r, errx.New(errx.KindConflict,
			"a device with that management IP is already managed"))
		return
	}
	if req.AttachTo != "" {
		if h.AttachAddress == nil {
			httpx.WriteError(w, r, errx.New(errx.KindTransient, "attaching addresses is unavailable"))
			return
		}
		if err := h.AttachAddress(r.Context(), req.AttachTo, found.IP); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		// The find is settled either way: it has been dealt with, and leaving
		// it pending would offer the same address again on the next sweep.
		if err := h.Svc.Repo.SetFoundState(r.Context(), found.ID, "approved"); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{
			"attached_to": req.AttachTo, "address": found.IP})
		return
	}

	// The check runs before anything is created, which is the only moment a
	// duplicate is cheap to avoid: afterwards it is two records, two schedules
	// and a history split down the middle.
	if !req.Force && h.IdentityMatch != nil && found.SysName != "" {
		id, dname, dip, match, err := h.IdentityMatch(r.Context(), found.SysName, "")
		if err == nil && id != "" {
			httpx.WriteJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]any{
					"code": "duplicate_identity",
					"message": found.IP + " reports the same " + match + " as " +
						dname + " (" + dip + "), so it is probably the same device on a second address",
					"existing_device_id": id,
					"existing_device":    dname,
					"existing_mgmt_ip":   dip,
					"matched_on":         match,
				},
			})
			return
		}
	}

	name := req.Name
	if name == "" {
		name = found.SysName
	}
	if name == "" {
		name = found.IP
	}
	siteID := req.SiteID
	if siteID == "" {
		siteID = found.SiteID
	}
	deviceID, err := h.Onboard(r.Context(), OnboardInput{
		Name: name, MgmtIP: found.IP, SiteID: siteID,
		CredentialID: found.CredentialID, ConnectorID: found.ConnectorID,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.Svc.Repo.SetFoundState(r.Context(), found.ID, "approved"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"device_id": deviceID})
}

func (h *DiscoveryHandler) ignore(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.Ignore(r.Context(), chi.URLParam(r, "id"), meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
