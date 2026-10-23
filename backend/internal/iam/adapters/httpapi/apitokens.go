package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/iam/app"
	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

// APITokenHandler — /users/me/tokens (doc 09 §3). Any authenticated user
// manages their own tokens; minting and revoking need a real session.
type APITokenHandler struct {
	Svc *app.APITokenService
}

func (h *APITokenHandler) Register(r chi.Router) {
	r.Get("/users/me/tokens", h.list)
	r.Group(func(ir chi.Router) {
		ir.Use(httpx.RequireInteractive)
		ir.Post("/users/me/tokens", h.create)
		ir.Delete("/users/me/tokens/{id}", h.revoke)
	})
}

type apiTokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func tokenView(t *domain.APIToken) apiTokenView {
	return apiTokenView{
		ID: t.ID, Name: t.Name, Scopes: t.Scopes, ExpiresAt: t.ExpiresAt,
		LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt, CreatedAt: t.CreatedAt,
	}
}

func (h *APITokenHandler) list(w http.ResponseWriter, r *http.Request) {
	toks, err := h.Svc.List(r.Context(), httpx.ClaimsFrom(r.Context()).Subject)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]apiTokenView, 0, len(toks))
	for _, t := range toks {
		out = append(out, tokenView(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *APITokenHandler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "invalid JSON body"))
		return
	}
	t, plain, err := h.Svc.Create(r.Context(), httpx.ClaimsFrom(r.Context()), app.CreateAPITokenCmd{
		Name: req.Name, Scopes: req.Scopes, ExpiresAt: req.ExpiresAt, Meta: meta(r),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// The plain value appears in this response and nowhere else, ever.
	httpx.WriteJSON(w, http.StatusCreated, struct {
		apiTokenView
		Token string `json:"token"`
	}{tokenView(t), plain})
}

func (h *APITokenHandler) revoke(w http.ResponseWriter, r *http.Request) {
	userID := httpx.ClaimsFrom(r.Context()).Subject
	if err := h.Svc.Revoke(r.Context(), userID, chi.URLParam(r, "id"), meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
