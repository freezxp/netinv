// netinv-api — REST API, authN/Z, RBAC, inventory, query proxy (doc 05 §2).
// Composition root: wiring only (doc 13 rule 4).
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/iam/adapters/httpapi"
	"github.com/freezxp/netinv/backend/internal/iam/adapters/lockout"
	iampg "github.com/freezxp/netinv/backend/internal/iam/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/iam/app"
	invhttp "github.com/freezxp/netinv/backend/internal/inventory/adapters/httpapi"
	invpg "github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/inventory/adapters/snmptest"
	invapp "github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/cryptox"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
	"github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/redisx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("api", func(ctx context.Context, rt *service.Runtime) error {
		dsn := os.Getenv("NETINV_PG_DSN")
		if dsn == "" {
			rt.Log.Warn("NETINV_PG_DSN not set — running without database (skeleton mode)")
			rt.Health.SetReady(true)
			<-ctx.Done()
			return nil
		}

		if err := pgx.Migrate(ctx, dsn, rt.Log); err != nil {
			return err
		}
		rt.Log.Info("database migrations up to date")

		pool, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return err
		}
		defer pool.Close()

		// JWT issuer: persistent Ed25519 seed in prod; ephemeral in dev.
		seedB64 := os.Getenv("NETINV_JWT_SIGNING_KEY")
		if seedB64 == "" {
			seed := make([]byte, 32)
			if _, err := rand.Read(seed); err != nil {
				return err
			}
			seedB64 = base64.StdEncoding.EncodeToString(seed)
			rt.Log.Warn("NETINV_JWT_SIGNING_KEY not set — using ephemeral key; sessions won't survive restarts")
		}
		issuer, err := authn.NewIssuerFromBase64(seedB64, 15*time.Minute)
		if err != nil {
			return err
		}

		var lock app.Lockout
		if addr := os.Getenv("NETINV_REDIS_ADDR"); addr != "" {
			rc, err := redisx.Connect(ctx, addr)
			if err != nil {
				return err
			}
			defer rc.Close()
			lock = &lockout.Redis{Client: rc}
			rt.Log.Info("redis connected", "addr", addr)
		} else {
			lock = lockout.NewMemory()
			rt.Log.Warn("NETINV_REDIS_ADDR not set — in-memory lockout (single replica only)")
		}

		auditor := &audit.PGWriter{Pool: pool, Log: rt.Log}
		authSvc := &app.AuthService{
			Users:  &iampg.UserRepo{Pool: pool},
			Tokens: &iampg.RefreshTokenRepo{Pool: pool},
			Lockout: lock, Issuer: issuer, Audit: auditor,
			Argon: authn.DefaultArgon2, Log: rt.Log,
		}
		if generated, err := authSvc.Bootstrap(ctx, os.Getenv("NETINV_ADMIN_PASSWORD")); err != nil {
			return err
		} else if generated != "" {
			// Printed once, deliberately: initial credential handover (doc 20 §12).
			rt.Log.Info("bootstrap admin user created — password change required at first login",
				"username", "admin", "password", generated)
		}

		// Master key for the credential vault (ADR-011).
		var keys cryptox.KeyProvider
		if kp, err := cryptox.LoadEnvMasterKey(); err == nil {
			keys = kp
		} else {
			raw := make([]byte, 32)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			keys, _ = cryptox.NewEnvMasterKey(map[int][]byte{1: raw}, 1)
			rt.Log.Warn("NETINV_MASTER_KEY not set — ephemeral vault key; stored credentials won't survive restarts")
		}

		checker, err := authz.NewPGChecker(ctx, pool, rt.Log)
		if err != nil {
			return err
		}
		userRepo := &iampg.UserRepo{Pool: pool}
		tokenRepo := &iampg.RefreshTokenRepo{Pool: pool}
		userSvc := &app.UserService{
			Users: userRepo, Tokens: tokenRepo, Audit: auditor,
			Argon: authn.DefaultArgon2, Log: rt.Log,
		}
		siteSvc := &invapp.SiteService{Repo: &invpg.SiteRepo{Pool: pool}, Audit: auditor}
		credSvc := &invapp.CredentialService{
			Vault: &invpg.EnvelopeVault{Pool: pool, Keys: keys},
			Tester: snmptest.Tester{}, Audit: auditor,
		}

		authH := &httpapi.Handler{
			Auth: authSvc, Verifier: issuer,
			SecureCookies: os.Getenv("NETINV_INSECURE_COOKIES") != "1",
		}
		userH := &httpapi.UserHandler{Svc: userSvc, Repo: userRepo, Checker: checker}
		invH := &invhttp.Handler{Sites: siteSvc, Creds: credSvc, Checker: checker}

		api := chi.NewRouter()
		authH.Register(api)
		api.Group(func(g chi.Router) {
			g.Use(httpx.RequireAuth(issuer))
			userH.Register(g)
			invH.Register(g)
		})
		root := chi.NewRouter()
		root.Use(httpx.TraceMiddleware)
		root.Mount("/api/v1", api)
		rt.Health.Handle("/api/v1/", root)

		rt.Health.SetReady(true)
		rt.Log.Info("api ready", "addr", rt.Cfg.HTTPAddr)
		<-ctx.Done()
		return nil
	})
}
