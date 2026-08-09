// Per-user UI preferences (doc 09 §14). The dashboard layout is the first
// consumer: which panels appear, in what order, and how each is configured.
//
// Stored server-side rather than in localStorage so a layout survives a
// different browser and can be reasoned about by an operator who is not
// sitting at the machine that made it.
package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

type PreferencesHandler struct {
	Pool *pgxpool.Pool
}

// maxPrefsBytes bounds what one user can store. Preferences are free-form
// jsonb the server never interprets, which makes them an inviting place to
// park arbitrary data; a dashboard layout is a few kilobytes at most.
const maxPrefsBytes = 64 * 1024

func (h *PreferencesHandler) Register(r chi.Router) {
	// No permission gate beyond authentication: these are the caller's own
	// preferences, and every role has them.
	r.Get("/users/me/preferences", h.get)
	r.Put("/users/me/preferences", h.put)
}

func (h *PreferencesHandler) get(w http.ResponseWriter, r *http.Request) {
	uid := httpx.ClaimsFrom(r.Context()).Subject
	if uid == "" {
		httpx.WriteError(w, r, errx.New(errx.KindUnauthorized, "not authenticated"))
		return
	}
	var prefs []byte
	err := h.Pool.QueryRow(r.Context(),
		`SELECT prefs FROM iam.user_preferences WHERE user_id = $1`, uid).Scan(&prefs)
	if err == pgx.ErrNoRows {
		// An empty object rather than 404: "you have not customised anything"
		// is a normal state, and a client should not have to treat first use
		// as an error.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, errx.Wrap(errx.KindTransient, err, "load preferences"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(prefs)
}

func (h *PreferencesHandler) put(w http.ResponseWriter, r *http.Request) {
	uid := httpx.ClaimsFrom(r.Context()).Subject
	if uid == "" {
		httpx.WriteError(w, r, errx.New(errx.KindUnauthorized, "not authenticated"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPrefsBytes+1))
	if err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "could not read body"))
		return
	}
	if len(body) > maxPrefsBytes {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid,
			"preferences exceed %d KB", maxPrefsBytes/1024))
		return
	}
	// Validated as an object, not merely as JSON. A bare array or string would
	// store fine and then break every client that expects to read a key from
	// it, at which point the damage is already persisted.
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "body must be a JSON object"))
		return
	}
	if _, err := h.Pool.Exec(r.Context(), `
		INSERT INTO iam.user_preferences (user_id, prefs, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id) DO UPDATE
		SET prefs = excluded.prefs, updated_at = now()`, uid, body); err != nil {
		httpx.WriteError(w, r, errx.Wrap(errx.KindTransient, err, "save preferences"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
