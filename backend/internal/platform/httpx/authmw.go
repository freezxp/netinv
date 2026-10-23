package httpx

import (
	"context"
	"net/http"

	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type claimsKey struct{}

// RequireAuth verifies the Bearer token and stores claims in the context.
func RequireAuth(verifier authn.TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if len(raw) <= len(prefix) || raw[:len(prefix)] != prefix {
				WriteError(w, r, errx.New(errx.KindUnauthorized, "missing bearer token"))
				return
			}
			claims, err := verifier.Verify(raw[len(prefix):])
			if err != nil {
				WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey{}, claims)))
		})
	}
}

// ClaimsFrom returns claims stored by RequireAuth (zero value if absent).
// WithClaims attaches claims to a context the way the auth middleware does.
// Exported so handlers can be tested without minting and signing a JWT, which
// would test the token library rather than the handler.
func WithClaims(ctx context.Context, c *authn.Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

func ClaimsFrom(ctx context.Context) *authn.Claims {
	if c, ok := ctx.Value(claimsKey{}).(*authn.Claims); ok {
		return c
	}
	return &authn.Claims{}
}

// RequirePerm gates a route on a permission (FR-RBAC-02: declared per route,
// fail-closed 403). An API token needs the permission in both its owner's
// roles and its own scopes.
func RequirePerm(checker authz.Checker, perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := ClaimsFrom(r.Context())
			if !checker.Has(claims.Roles, perm) || !claims.Allows(perm) {
				WriteError(w, r, errx.New(errx.KindForbidden, "missing permission: %s", perm))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireInteractive refuses API tokens. For actions that act on the caller's
// own credentials, where a leaked token must not be able to entrench itself.
func RequireInteractive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ClaimsFrom(r.Context()).Interactive() {
			WriteError(w, r, errx.New(errx.KindForbidden, "not available to API tokens; sign in"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
