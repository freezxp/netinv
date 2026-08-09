package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

func asUser(r *http.Request, uid string) *http.Request {
	c := &authn.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: uid}}
	return r.WithContext(httpx.WithClaims(r.Context(), c))
}

func seedUser(t *testing.T, h *PreferencesHandler, uid string) {
	t.Helper()
	if _, err := h.Pool.Exec(t.Context(), `
		INSERT INTO iam.users
			(id, tenant_id, username, email, display_name, password_hash, status)
		VALUES ($1,'t_default',$2,$3,$4,'x','active')
		ON CONFLICT (id) DO NOTHING`,
		uid, "u-"+uid, uid+"@example.invalid", "User "+uid); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

// First use is a normal state, not an error: a client should not have to treat
// "you have never customised anything" as a 404 to handle.
func TestGetReturnsEmptyObjectBeforeAnythingIsSaved(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	h := &PreferencesHandler{Pool: pool}
	seedUser(t, h, "u_fresh")

	w := httptest.NewRecorder()
	h.get(w, asUser(httptest.NewRequest(http.MethodGet, "/users/me/preferences", nil), "u_fresh"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "{}" {
		t.Errorf("body = %q, want an empty object", got)
	}
}

func TestPreferencesRoundTrip(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	h := &PreferencesHandler{Pool: pool}
	seedUser(t, h, "u_rt")

	body := `{"theme":"dark","dashboard":{"panels":[{"id":"a","kind":"status"}]}}`
	w := httptest.NewRecorder()
	h.put(w, asUser(httptest.NewRequest(http.MethodPut, "/users/me/preferences",
		strings.NewReader(body)), "u_rt"))
	if w.Code != http.StatusOK {
		t.Fatalf("put status = %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.get(w, asUser(httptest.NewRequest(http.MethodGet, "/users/me/preferences", nil), "u_rt"))

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	if got["theme"] != "dark" {
		t.Errorf("theme = %v, want dark", got["theme"])
	}
	if got["dashboard"] == nil {
		t.Error("dashboard layout did not survive the round trip")
	}
}

// A bare array or string is valid JSON and would store happily, then break
// every client that reads a key from it — after the damage is persisted.
func TestPutRejectsNonObjectBodies(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	h := &PreferencesHandler{Pool: pool}
	seedUser(t, h, "u_bad")

	for _, body := range []string{`["a"]`, `"str"`, `42`, `not json`, ``} {
		w := httptest.NewRecorder()
		h.put(w, asUser(httptest.NewRequest(http.MethodPut, "/users/me/preferences",
			strings.NewReader(body)), "u_bad"))
		if w.Code < 400 {
			t.Errorf("body %q was accepted with status %d", body, w.Code)
		}
	}
}

// Preferences are free-form jsonb the server never interprets, which makes
// them an inviting place to park arbitrary data.
func TestPutRejectsOversizedPreferences(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	h := &PreferencesHandler{Pool: pool}
	seedUser(t, h, "u_big")

	huge := `{"x":"` + strings.Repeat("a", maxPrefsBytes) + `"}`
	w := httptest.NewRecorder()
	h.put(w, asUser(httptest.NewRequest(http.MethodPut, "/users/me/preferences",
		strings.NewReader(huge)), "u_big"))
	if w.Code < 400 {
		t.Errorf("status = %d, want a rejection for %d bytes", w.Code, len(huge))
	}
}

// Preferences belong to a subject. Without one there is nothing to read or
// write, and falling through would touch a row keyed by the empty string.
func TestUnauthenticatedIsRejected(t *testing.T) {
	h := &PreferencesHandler{}
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request){
		"get": h.get, "put": h.put,
	} {
		w := httptest.NewRecorder()
		fn(w, httptest.NewRequest(http.MethodGet, "/users/me/preferences",
			strings.NewReader("{}")))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, w.Code)
		}
	}
}
