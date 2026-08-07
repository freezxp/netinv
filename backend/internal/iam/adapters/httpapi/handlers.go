// Package httpapi — IAM HTTP handlers (doc 09 §2). The refresh token rides an
// httpOnly Secure cookie scoped to /api/v1/auth (doc 20 §3).
package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/iam/app"
	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

const refreshCookie = "netinv_refresh"

type Handler struct {
	Auth     *app.AuthService
	Verifier authn.TokenVerifier
	// SecureCookies is disabled only for plain-HTTP local dev.
	SecureCookies bool
}

// Register adds the auth routes to the shared /api/v1 router.
func (h *Handler) Register(r chi.Router) {
	r.Post("/auth/login", h.login)
	r.Post("/auth/refresh", h.refresh)
	r.Post("/auth/logout", h.logout)
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequireAuth(h.Verifier))
		pr.Get("/auth/me", h.me)
	})
}

func clientIP(r *http.Request) string {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return ip
}

func (h *Handler) meta(r *http.Request) app.ClientMeta {
	return app.ClientMeta{SourceIP: clientIP(r), UserAgent: r.UserAgent(), TraceID: httpx.TraceID(r.Context())}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type userPayload struct {
	ID                     string   `json:"id"`
	Username               string   `json:"username"`
	DisplayName            string   `json:"display_name"`
	Roles                  []string `json:"roles"`
	PasswordChangeRequired bool     `json:"password_change_required"`
}

type tokenResponse struct {
	AccessToken string      `json:"access_token"`
	TokenType   string      `json:"token_type"`
	ExpiresIn   int         `json:"expires_in"`
	User        userPayload `json:"user"`
}

func toResponse(res *app.LoginResult) tokenResponse {
	return tokenResponse{
		AccessToken: res.AccessToken, TokenType: "Bearer", ExpiresIn: res.ExpiresIn,
		User: userPayload{
			ID: res.User.ID, Username: res.User.Username, DisplayName: res.User.DisplayName,
			Roles: res.Roles, PasswordChangeRequired: res.PasswordChangeRequired,
		},
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Username == "" || req.Password == "" {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "username and password are required"))
		return
	}
	res, err := h.Auth.Login(r.Context(), req.Username, req.Password, h.meta(r))
	if err != nil {
		if errors.Is(err, app.ErrLocked) {
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					TraceID string `json:"trace_id"`
				} `json:"error"`
			}
			body.Error.Code = "account_locked"
			body.Error.Message = "account temporarily locked after failed attempts"
			body.Error.TraceID = httpx.TraceID(r.Context())
			httpx.WriteJSON(w, http.StatusLocked, body)
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	h.setRefreshCookie(w, res.RefreshPlain)
	httpx.WriteJSON(w, http.StatusOK, toResponse(res))
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(refreshCookie)
	if err != nil || c.Value == "" {
		httpx.WriteError(w, r, errx.New(errx.KindUnauthorized, "missing refresh token"))
		return
	}
	res, err := h.Auth.Refresh(r.Context(), c.Value, h.meta(r))
	if err != nil {
		h.clearRefreshCookie(w)
		httpx.WriteError(w, r, err)
		return
	}
	h.setRefreshCookie(w, res.RefreshPlain)
	httpx.WriteJSON(w, http.StatusOK, toResponse(res))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(refreshCookie); err == nil && c.Value != "" {
		h.Auth.Logout(r.Context(), c.Value, h.meta(r))
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	claims := httpx.ClaimsFrom(r.Context())
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": claims.Subject, "username": claims.Username,
		"roles": claims.Roles, "tenant": claims.Tenant,
	})
}

func (h *Handler) setRefreshCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookie, Value: value, Path: "/api/v1/auth",
		HttpOnly: true, Secure: h.SecureCookies, SameSite: http.SameSiteStrictMode,
		MaxAge: int((30 * 24 * time.Hour).Seconds()),
	})
}

func (h *Handler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookie, Value: "", Path: "/api/v1/auth",
		HttpOnly: true, Secure: h.SecureCookies, SameSite: http.SameSiteStrictMode,
		MaxAge: -1,
	})
}

