// netinv-api — REST API, authN/Z, RBAC, inventory, query proxy (doc 05 §2).
// Composition root: wiring only (doc 13 rule 4).
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"

	"github.com/freezxp/netinv/backend/internal/audit"
	colamqp "github.com/freezxp/netinv/backend/internal/collection/adapters/amqp"
	colhttp "github.com/freezxp/netinv/backend/internal/collection/adapters/httpapi"
	colpg "github.com/freezxp/netinv/backend/internal/collection/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/collection/adapters/secrets"
	colapp "github.com/freezxp/netinv/backend/internal/collection/app"
	"github.com/freezxp/netinv/backend/internal/iam/adapters/httpapi"
	"github.com/freezxp/netinv/backend/internal/iam/adapters/lockout"
	iampg "github.com/freezxp/netinv/backend/internal/iam/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/iam/app"
	invhttp "github.com/freezxp/netinv/backend/internal/inventory/adapters/httpapi"
	invpg "github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/inventory/adapters/snmptest"
	invapp "github.com/freezxp/netinv/backend/internal/inventory/app"
	invdomain "github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/cryptox"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/redisx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
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

		var redisClient *redis.Client
		var lock app.Lockout
		if addr := os.Getenv("NETINV_REDIS_ADDR"); addr != "" {
			redisClient, err = redisx.Connect(ctx, addr)
			if err != nil {
				return err
			}
			defer redisClient.Close()
			lock = &lockout.Redis{Client: redisClient}
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
		vault := &invpg.EnvelopeVault{Pool: pool, Keys: keys}

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
		// Temporary connector-catalog seed until the registry-driven seeding
		// lands with the vendor connectors (Sprint 17).
		if _, err := pool.Exec(ctx, `
			INSERT INTO platform.connectors (id, vendor, display_name, version, capabilities)
			VALUES ('generic', 'Generic', 'Generic SNMP (IF-MIB)', '0.2.0',
			        '["inventory","interfaces","topology","icmp"]')
			ON CONFLICT (id) DO UPDATE SET version = excluded.version,
				capabilities = excluded.capabilities`); err != nil {
			return err
		}

		siteSvc := &invapp.SiteService{Repo: &invpg.SiteRepo{Pool: pool}, Audit: auditor}
		credSvc := &invapp.CredentialService{
			Vault: vault, Tester: snmptest.Tester{}, Audit: auditor,
		}
		devSvc := &invapp.DeviceService{Repo: &invpg.DeviceRepo{Pool: pool}, Audit: auditor}
		pollerSvc := &colapp.PollerService{Repo: &colpg.PollerRepo{Pool: pool}, Audit: auditor}

		authH := &httpapi.Handler{
			Auth: authSvc, Verifier: issuer,
			SecureCookies: os.Getenv("NETINV_INSECURE_COOKIES") != "1",
		}
		userH := &httpapi.UserHandler{Svc: userSvc, Repo: userRepo, Checker: checker}
		invH := &invhttp.Handler{Sites: siteSvc, Creds: credSvc, Checker: checker}
		devH := &invhttp.DeviceHandler{Svc: devSvc, Checker: checker}
		pollerH := &colhttp.PollerHandler{Svc: pollerSvc, Checker: checker}

		// AMQP: sync-result consumer + on-demand sync dispatch (doc 11).
		if amqpURL := os.Getenv("NETINV_AMQP_URL"); amqpURL != "" && redisClient != nil {
			mq, err := amqpx.Connect(ctx, amqpURL)
			if err != nil {
				return err
			}
			defer mq.Close()
			if err := mq.EnsureSyncResultsQueue(); err != nil {
				return err
			}
			syncSvc := &invapp.SyncService{
				Repo:  &invpg.SyncRepo{Pool: pool},
				Locks: redisLocker(redisClient),
				Audit: auditor, Log: rt.Log,
			}
			deliveries, err := mq.Consume(amqpx.SyncResultsQueue, 8)
			if err != nil {
				return err
			}
			go consumeSyncResults(ctx, deliveries, syncSvc, rt)

			dispatcher := &colamqp.SyncDispatcher{
				Client: mq, Secrets: &secrets.Resolver{Vault: vault},
			}
			devH.DispatchSync = func(dctx context.Context, d *invdomain.Device) (string, error) {
				port := 0
				if v, ok := d.Attrs["snmp_port"].(float64); ok {
					port = int(v)
				}
				return dispatcher.DispatchSync(dctx, colamqp.SyncTarget{
					DeviceID: d.ID, SiteID: d.SiteID, MgmtIP: d.MgmtIP, Port: port,
					ConnectorID: d.ConnectorID, CredentialID: d.CredentialID,
				})
			}
			rt.Log.Info("sync consumer and dispatch enabled")
		} else {
			rt.Log.Warn("NETINV_AMQP_URL or redis missing — sync consumer and on-demand sync disabled")
		}

		api := chi.NewRouter()
		authH.Register(api)
		pollerH.RegisterPublic(api) // token-authenticated, not JWT
		api.Group(func(g chi.Router) {
			g.Use(httpx.RequireAuth(issuer))
			userH.Register(g)
			invH.Register(g)
			devH.Register(g)
			pollerH.RegisterAuthed(g)
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

func redisLocker(client *redis.Client) *invpg.RedisLocker {
	return &invpg.RedisLocker{Try: func(ctx context.Context, key string,
		ttl time.Duration) (func(), bool, error) {
		lock, ok, err := redisx.TryLock(ctx, client, key, ttl)
		if err != nil || !ok {
			return nil, ok, err
		}
		return func() { _ = lock.Release(context.WithoutCancel(ctx)) }, true, nil
	}}
}

// consumeSyncResults drives the sync pipeline: at-least-once with per-device
// locks; lock contention requeues, poison rejects (doc 23 §3).
func consumeSyncResults(ctx context.Context, deliveries <-chan amqp091.Delivery,
	svc *invapp.SyncService, rt *service.Runtime) {
	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-deliveries:
			if !ok {
				return
			}
			var res wire.SyncResult
			if err := json.Unmarshal(d.Body, &res); err != nil {
				rt.Log.Warn("malformed sync result dropped", "err", err)
				_ = d.Reject(false)
				continue
			}
			err := svc.HandleResult(ctx, res, id.New("sr"))
			switch {
			case err == nil:
				_ = d.Ack(false)
			case errx.KindOf(err) == errx.KindConflict,
				errx.KindOf(err) == errx.KindTransient:
				rt.Log.Warn("sync result requeued", "device", res.DeviceID, "err", err)
				time.Sleep(time.Second)
				_ = d.Nack(false, true)
			default:
				rt.Log.Error("sync result failed permanently", "device", res.DeviceID, "err", err)
				_ = d.Reject(false)
			}
		}
	}
}
