package maps

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/audit"
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
	// Audit records destructive changes. Optional so tests can omit it.
	Audit audit.Writer
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
		pw.Post("/maps/generate", h.generate)
		pw.Put("/maps/{id}/draft", h.saveDraft)
		pw.Post("/maps/{id}/publish", h.publish)
		pw.Get("/maps/{id}/suggestions", h.suggestions)
		pw.Delete("/maps/{id}", h.del)
	})
}

// generate builds a draft map from LLDP adjacencies (FR-MAP-06). The first
// map is the expensive one — placing every device and binding every link
// before seeing anything — and the device already reports the topology.
func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Name == "" {
		req.Name = "Discovered topology"
	}
	meta, nodes, links, err := h.Store.GenerateFromTopology(
		r.Context(), req.Name, httpx.ClaimsFrom(r.Context()).Subject)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": meta.ID, "name": meta.Name, "draft_rev": meta.DraftRev,
		"nodes": nodes, "links": links,
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
	id := chi.URLParam(r, "id")
	// Read the name first: deleting takes every revision with it, and an audit
	// row naming only an id nobody can look up afterwards is close to useless.
	name := h.Store.Name(r.Context(), id)
	if err := h.Store.Delete(r.Context(), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if h.Audit != nil {
		ip := r.RemoteAddr
		if i := strings.LastIndex(ip, ":"); i > 0 {
			ip = ip[:i]
		}
		h.Audit.Write(r.Context(), audit.Event{
			ActorKind: "user", ActorID: httpx.ClaimsFrom(r.Context()).Subject,
			Action: "map.delete", ResourceKind: "map", ResourceID: id,
			Before:   map[string]any{"name": name},
			SourceIP: ip, UserAgent: r.UserAgent(),
			TraceID: httpx.TraceID(r.Context()),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}
