// Package httpapi — notification channel management (doc 09 §11; Admin only).
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	notifpg "github.com/freezxp/netinv/backend/internal/notify/adapters/postgres"
	notifapp "github.com/freezxp/netinv/backend/internal/notify/app"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

type Handler struct {
	Repo    *notifpg.ChannelRepo
	Senders map[string]notifapp.Sender
	Checker authz.Checker
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(httpx.RequirePerm(h.Checker, authz.SettingsWrite))
		g.Get("/notification-channels", h.list)
		g.Post("/notification-channels", h.create)
		g.Delete("/notification-channels/{id}", h.del)
		g.Post("/notification-channels/{id}/test", h.test)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	channels, err := h.Repo.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": channels})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string            `json:"name"`
		Kind   string            `json:"kind"`
		Config map[string]any    `json:"config"`
		Secret map[string]string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.Kind == "" {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "name and kind are required"))
		return
	}
	switch req.Kind {
	case "email", "webhook", "slack":
	default:
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "kind must be email, webhook or slack"))
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	cid, err := h.Repo.Create(r.Context(), req.Name, req.Kind, req.Config, req.Secret)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"id": cid})
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request) {
	if err := h.Repo.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// test sends a synthetic notification through the real sender (FR-NOT-05).
func (h *Handler) test(w http.ResponseWriter, r *http.Request) {
	channels, err := h.Repo.EnabledChannels(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cid := chi.URLParam(r, "id")
	for _, ch := range channels {
		if ch.ID != cid {
			continue
		}
		sender, ok := h.Senders[ch.Kind]
		if !ok {
			httpx.WriteError(w, r, errx.New(errx.KindInvalid, "no sender for kind %s", ch.Kind))
			return
		}
		ev := wire.AlertEvent{
			Event: "alert.fired", AlertID: "test", RuleName: "Test notification",
			Severity: "info", Summary: "NetInv channel test — configuration works.",
		}
		if err := sender.Send(r.Context(), ch, ev); err != nil {
			httpx.WriteJSON(w, http.StatusOK, map[string]string{
				"result": "failed", "detail": err.Error()})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"result": "ok"})
		return
	}
	httpx.WriteError(w, r, errx.New(errx.KindNotFound, "channel not found or disabled"))
}
