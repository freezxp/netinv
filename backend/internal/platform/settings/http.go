package settings

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

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
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequirePerm(h.Checker, authz.PlatformRead))
		pr.Get("/settings/metrics-mirror", h.getMirror)
		pr.Get("/settings/branding", h.getBranding)
	})
	r.Group(func(pw chi.Router) {
		pw.Use(httpx.RequirePerm(h.Checker, authz.SettingsWrite))
		pw.Put("/settings/metrics-mirror", h.putMirror)
		pw.Post("/settings/metrics-mirror/test", h.testMirror)
		pw.Put("/settings/branding", h.putBranding)
	})
}

func (h *Handler) getMirror(w http.ResponseWriter, r *http.Request) {
	m, err := h.Store.GetMirror(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}

// getBranding returns what exported reports say about who produced them.
// Readable at platform:read rather than settings:write, because the Reports
// page shows the operator what their export will look like.
func (h *Handler) getBranding(w http.ResponseWriter, r *http.Request) {
	b, err := h.Store.GetBranding(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, b)
}

func (h *Handler) putBranding(w http.ResponseWriter, r *http.Request) {
	var in Branding
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	actor := httpx.ClaimsFrom(r.Context()).Subject
	out, err := h.Store.PutBranding(r.Context(), in, actor)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// The logo is deliberately not in the audit detail: it is up to half a
	// megabyte of base64 and would bury every other entry in the log.
	h.Audit.Write(r.Context(), audit.Event{
		ActorKind: "user", ActorID: actor, Action: "settings.branding",
		ResourceKind: "setting", ResourceID: BrandingKey,
		After: map[string]any{"company": out.Company, "subtitle": out.Subtitle,
			"footer": out.Footer, "accent": out.Accent, "has_logo": out.Logo != ""},
	})
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) putMirror(w http.ResponseWriter, r *http.Request) {
	var in Mirror
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	before, _ := h.Store.GetMirror(r.Context())
	actor := httpx.ClaimsFrom(r.Context()).Subject
	out, err := h.Store.PutMirror(r.Context(), in, actor)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// Where a copy of every metric is sent is worth an audit entry with both
	// sides: it is a data-egress decision, not a display preference.
	h.Audit.Write(r.Context(), audit.Event{
		ActorKind: "user", ActorID: actor, Action: "settings.metrics_mirror",
		ResourceKind: "setting", ResourceID: MirrorKey,
		Before: map[string]any{"enabled": before.Enabled, "urls": before.URLs},
		After:  map[string]any{"enabled": out.Enabled, "urls": out.URLs},
	})
	httpx.WriteJSON(w, http.StatusOK, out)
}

// testMirror probes one destination the way the writers use it, so a typo is
// found while someone is looking at the form rather than an hour later in a
// counter nobody is watching.
func (h *Handler) testMirror(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.URL == "" {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "url is required"))
		return
	}
	if err := validateURL(in.URL); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	// An empty import body: accepted by a healthy VictoriaMetrics, and it
	// writes nothing. Testing with a real sample would put a fabricated series
	// into the operator's backup, which is a strange thing for a test button
	// to leave behind.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		in.URL+"/api/v1/import", http.NoBody)
	if err != nil {
		httpx.WriteError(w, r, errx.Wrap(errx.KindInternal, err, "probe"))
		return
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"ok": false, "detail": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode >= 300 {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"detail": http.StatusText(resp.StatusCode) + ": " + string(body),
		})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "detail": "accepted an import request"})
}
