package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
)

// A token's scopes subtract from its owner's roles and never add: both must
// grant the permission.
func TestRequirePermScopes(t *testing.T) {
	checker := authz.Static{"operator": {authz.DevicesRead, authz.DevicesWrite}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	cases := []struct {
		name   string
		claims *authn.Claims
		perm   string
		want   int
	}{
		{"session, role grants", &authn.Claims{Roles: []string{"operator"}}, authz.DevicesWrite, 200},
		{"token, scope and role grant", &authn.Claims{Roles: []string{"operator"}, APITokenID: "tok_1", Scopes: []string{authz.DevicesRead}}, authz.DevicesRead, 200},
		{"token, role grants but scope does not", &authn.Claims{Roles: []string{"operator"}, APITokenID: "tok_1", Scopes: []string{authz.DevicesRead}}, authz.DevicesWrite, 403},
		{"token, scope grants but role does not", &authn.Claims{Roles: []string{"operator"}, APITokenID: "tok_1", Scopes: []string{authz.UsersWrite}}, authz.UsersWrite, 403},
		{"token with empty scopes", &authn.Claims{Roles: []string{"operator"}, APITokenID: "tok_1", Scopes: []string{}}, authz.DevicesRead, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(WithClaims(t.Context(), c.claims))
			RequirePerm(checker, c.perm)(ok).ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestRequireInteractive(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, c := range []struct {
		claims *authn.Claims
		want   int
	}{
		{&authn.Claims{}, 200},
		{&authn.Claims{APITokenID: "tok_1", Scopes: []string{}}, 403},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(WithClaims(t.Context(), c.claims))
		RequireInteractive(ok).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("api token %q: status = %d, want %d", c.claims.APITokenID, rec.Code, c.want)
		}
	}
}

type stubVerifier string

func (s stubVerifier) Verify(string) (*authn.Claims, error) {
	return &authn.Claims{Username: string(s)}, nil
}

func TestChainRoutesByPrefix(t *testing.T) {
	ch := authn.Chain{JWT: stubVerifier("jwt"), API: stubVerifier("api")}
	for raw, want := range map[string]string{"nv_abc": "api", "eyJhbGciOi.x.y": "jwt"} {
		c, _ := ch.Verify(raw)
		if c.Username != want {
			t.Errorf("%s routed to %s, want %s", raw, c.Username, want)
		}
	}
}
