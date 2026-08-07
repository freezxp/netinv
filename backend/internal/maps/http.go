package maps

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

type Handler struct {
	Store   *Store
	Live    *LiveAssembler
	Checker authz.Checker
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequirePerm(h.Checker, authz.MapsRead))
		pr.Get("/maps", h.list)
		pr.Get("/maps/{id}", h.get)
		pr.Get("/maps/{id}/live", h.live)
	})
	r.Group(func(pw chi.Router) {
		pw.Use(httpx.RequirePerm(h.Checker, authz.MapsWrite))
		pw.Post("/maps", h.create)
		pw.Put("/maps/{id}/draft", h.saveDraft)
		pw.Post("/maps/{id}/publish", h.publish)
		pw.Get("/maps/{id}/suggestions", h.suggestions)
		pw.Delete("/maps/{id}", h.del)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.Store.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "name is required"))
		return
	}
	m, err := h.Store.Create(r.Context(), req.Name, httpx.ClaimsFrom(r.Context()).Subject)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, m)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	which := "published"
	if r.URL.Query().Get("rev") == "draft" {
		which = "draft"
	}
	def, rev, err := h.Store.Load(r.Context(), chi.URLParam(r, "id"), which)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"rev": rev, "definition": def})
}

func (h *Handler) saveDraft(w http.ResponseWriter, r *http.Request) {
	var def Definition
	if err := json.NewDecoder(r.Body).Decode(&def); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed map definition"))
		return
	}
	if err := h.Store.SaveDraft(r.Context(), chi.URLParam(r, "id"), &def,
		httpx.ClaimsFrom(r.Context()).Subject); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	rev, err := h.Store.Publish(r.Context(), chi.URLParam(r, "id"),
		httpx.ClaimsFrom(r.Context()).Subject)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"published_rev": rev})
}

func (h *Handler) live(w http.ResponseWriter, r *http.Request) {
	data, err := h.Live.Live(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, data)
}

func (h *Handler) suggestions(w http.ResponseWriter, r *http.Request) {
	items, err := h.Store.Suggestions(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
