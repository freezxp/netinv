// Package secrets — adapts the inventory credential vault to the scheduler's
// SecretResolver port (cross-context via interface, wired in cmd — doc 13).
// Decrypted credentials are cached briefly to avoid per-job crypto.
package secrets

import (
	"context"
	"sync"
	"time"

	invapp "github.com/freezxp/netinv/backend/internal/inventory/app"
	invdomain "github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

type Resolver struct {
	Vault invapp.CredentialVault
	TTL   time.Duration // cache TTL, default 60s

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	cred wire.SNMPCred
	at   time.Time
}

func (r *Resolver) Resolve(ctx context.Context, credentialID string) (wire.SNMPCred, error) {
	if r.TTL == 0 {
		r.TTL = time.Minute
	}
	r.mu.Lock()
	if c, ok := r.cache[credentialID]; ok && time.Since(c.at) < r.TTL {
		r.mu.Unlock()
		return c.cred, nil
	}
	r.mu.Unlock()

	meta, err := r.Vault.Get(ctx, credentialID)
	if err != nil {
		return wire.SNMPCred{}, err
	}
	secret, err := r.Vault.Decrypt(ctx, credentialID)
	if err != nil {
		return wire.SNMPCred{}, err
	}
	cred := toWire(meta.Kind, secret)

	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cached{}
	}
	r.cache[credentialID] = cached{cred: cred, at: time.Now()}
	r.mu.Unlock()
	return cred, nil
}

func toWire(kind invdomain.CredentialKind, s invdomain.Secret) wire.SNMPCred {
	if kind == invdomain.SNMPv2c {
		return wire.SNMPCred{Version: "v2c", Community: s.Community}
	}
	return wire.SNMPCred{
		Version: "v3", Username: s.Username,
		AuthProto: s.AuthProtocol, AuthPass: s.AuthPassword,
		PrivProto: s.PrivProtocol, PrivPass: s.PrivPassword,
		Context: s.Context,
	}
}
