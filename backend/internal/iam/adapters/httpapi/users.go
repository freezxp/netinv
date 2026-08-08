package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/iam/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/iam/app"
	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

// UserHandler — /users and /roles (doc 09 §3), Admin-gated per doc 20 §5.
type UserHandler struct {
	Svc     *app.UserService
	Repo    *postgres.UserRepo // read-side queries
	Checker authz.Checker
}

// Register adds user-management routes to an already-authenticated router.
func (h *UserHandler) Register(r chi.Router) {
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequirePerm(h.Checker, authz.UsersRead))
		pr.Get("/users", h.list)
		pr.Get("/users/{id}", h.get)
		pr.Get("/roles", h.roles)
	})
	r.Group(func(pw chi.Router) {
		pw.Use(httpx.RequirePerm(h.Checker, authz.UsersWrite))
		pw.Post("/users", h.create)
		pw.Patch("/users/{id}", h.update)
		pw.Put("/users/{id}/roles", h.setRoles)
		pw.Post("/users/{id}/deactivate", h.deactivate)
		pw.Post("/users/{id}/activate", h.activate)
		pw.Post("/users/{id}/reset-password", h.resetPassword)
	})
	// Any authenticated user: own password change (FR-AUTH; doc 09 §2).
	r.Put("/auth/me/password", h.changeOwnPassword)
}

func meta(r *http.Request) app.ClientMeta {
	return app.ClientMeta{
		SourceIP:  clientIP(r),
		UserAgent: r.UserAgent(),
		TraceID:   httpx.TraceID(r.Context()),
	}
}

type userView struct {
	ID                     string   `json:"id"`
	Username               string   `json:"username"`
	Email                  string   `json:"email"`
	DisplayName            string   `json:"display_name"`
	Status                 string   `json:"status"`
	PasswordChangeRequired bool     `json:"password_change_required"`
	LastLoginAt            *string  `json:"last_login_at"`
	Roles                  []string `json:"roles"`
}

func (h *UserHandler) view(r *http.Request, u *domain.User) userView {
	roles, _ := h.Repo.RoleNames(r.Context(), u.ID)
	var last *string
	if u.LastLoginAt != nil {
		s := u.LastLoginAt.UTC().Format("2006-01-02T15:04:05Z")
		last = &s
	}
	return userView{
		ID: u.ID, Username: u.Username, Email: u.Email, DisplayName: u.DisplayName,
		Status: string(u.Status), PasswordChangeRequired: u.PasswordChangeRequired,
		LastLoginAt: last, Roles: roles,
	}
}

func (h *UserHandler) list(w http.ResponseWriter, r *http.Request) {
	users, err := h.Repo.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]userView, 0, len(users))
	for _, u := range users {
		out = append(out, h.view(r, u))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nil})
}

func (h *UserHandler) get(w http.ResponseWriter, r *http.Request) {
	u, err := h.Repo.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.view(r, u))
}

func (h *UserHandler) roles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.Repo.Roles(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": roles})
}

func (h *UserHandler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username     string   `json:"username"`
		Email        string   `json:"email"`
		DisplayName  string   `json:"display_name"`
		Roles        []string `json:"roles"`
		TempPassword string   `json:"temporary_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	u, temp, err := h.Svc.CreateUser(r.Context(), app.CreateUserCmd{
		Username: req.Username, Email: req.Email, DisplayName: req.DisplayName,
		RoleIDs: req.Roles, TempPassword: req.TempPassword,
		Actor: httpx.ClaimsFrom(r.Context()).Subject, Meta: meta(r),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp := map[string]any{"user": h.view(r, u)}
	if req.TempPassword == "" {
		resp["temporary_password"] = temp // shown once (doc 09 §3)
	}
	w.Header().Set("Location", "/api/v1/users/"+u.ID)
	httpx.WriteJSON(w, http.StatusCreated, resp)
}

func (h *UserHandler) update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       *string `json:"email"`
		DisplayName *string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	uid := chi.URLParam(r, "id")
	if err := h.Repo.UpdateProfile(r.Context(), uid, req.Email, req.DisplayName); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	u, err := h.Repo.GetByID(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.view(r, u))
}

func (h *UserHandler) setRoles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Roles []string `json:"roles"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Roles) == 0 {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "roles array is required"))
		return
	}
	uid := chi.URLParam(r, "id")
	if err := h.Svc.SetRoles(r.Context(), uid, req.Roles,
		httpx.ClaimsFrom(r.Context()).Subject, meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	u, err := h.Repo.GetByID(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.view(r, u))
}

func (h *UserHandler) deactivate(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.Deactivate(r.Context(), chi.URLParam(r, "id"),
		httpx.ClaimsFrom(r.Context()).Subject, meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *UserHandler) activate(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.Activate(r.Context(), chi.URLParam(r, "id"),
		httpx.ClaimsFrom(r.Context()).Subject, meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *UserHandler) resetPassword(w http.ResponseWriter, r *http.Request) {
	temp, err := h.Svc.ResetPassword(r.Context(), chi.URLParam(r, "id"),
		httpx.ClaimsFrom(r.Context()).Subject, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"temporary_password": temp})
}

func (h *UserHandler) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	if err := h.Svc.ChangeOwnPassword(r.Context(),
		httpx.ClaimsFrom(r.Context()).Subject, req.OldPassword, req.NewPassword, meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
