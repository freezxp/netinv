package app

import (
	"context"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// CredentialVault seals/unseals credential secrets (doc 17 §2). The postgres
// adapter implements it over cryptox envelopes (ADR-011).
type CredentialVault interface {
	List(ctx context.Context) ([]*domain.Credential, error)
	Get(ctx context.Context, id string) (*domain.Credential, error)
	Store(ctx context.Context, name string, kind domain.CredentialKind,
		secret domain.Secret, createdBy string) (*domain.Credential, error)
	UpdateSecret(ctx context.Context, id string, secret domain.Secret) error
	Rename(ctx context.Context, id, name string) error
	Delete(ctx context.Context, id string) error // errx.Conflict while referenced
	// Decrypt is the use-time path (poller jobs, credential test) — never HTTP.
	Decrypt(ctx context.Context, id string) (domain.Secret, error)
}

// SNMPTester probes a target with a credential (FR-CRED-03).
type SNMPTester interface {
	Test(ctx context.Context, target string, port int,
		kind domain.CredentialKind, secret domain.Secret) TestResult
}

type TestResult struct {
	Result    string `json:"result"` // ok | timeout | auth_failure | priv_failure | error
	SysName   string `json:"sys_name,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type CredentialService struct {
	Vault  CredentialVault
	Tester SNMPTester
	Audit  audit.Writer
}

func validateSecret(kind domain.CredentialKind, s domain.Secret) error {
	switch kind {
	case domain.SNMPv2c:
		if s.Community == "" {
			return errx.New(errx.KindInvalid, "community is required for snmp_v2c")
		}
	case domain.SNMPv3:
		if s.Username == "" || s.AuthProtocol == "" || s.AuthPassword == "" {
			return errx.New(errx.KindInvalid, "username, auth_protocol and auth_password are required for snmp_v3")
		}
		switch s.AuthProtocol {
		case "sha1", "sha256", "md5":
		default:
			return errx.New(errx.KindInvalid, "auth_protocol must be sha1, sha256 or md5")
		}
		if s.PrivProtocol != "" {
			switch s.PrivProtocol {
			case "aes128", "aes256", "des":
			default:
				return errx.New(errx.KindInvalid, "priv_protocol must be aes128, aes256 or des")
			}
			if s.PrivPassword == "" {
				return errx.New(errx.KindInvalid, "priv_password required with priv_protocol")
			}
		}
	default:
		return errx.New(errx.KindInvalid, "kind must be snmp_v2c or snmp_v3")
	}
	return nil
}

func (c *CredentialService) Create(ctx context.Context, name string,
	kind domain.CredentialKind, secret domain.Secret, m Meta) (*domain.Credential, error) {
	if name == "" {
		return nil, errx.New(errx.KindInvalid, "name is required")
	}
	if err := validateSecret(kind, secret); err != nil {
		return nil, err
	}
	cred, err := c.Vault.Store(ctx, name, kind, secret, m.Actor)
	if err != nil {
		return nil, err
	}
	// Audit metadata only — never secret values (FR-CRED-04).
	c.Audit.Write(ctx, m.event("credential.create", "credential", cred.ID, nil,
		map[string]any{"name": name, "kind": string(kind)}))
	return cred, nil
}

func (c *CredentialService) UpdateSecret(ctx context.Context, id string, secret domain.Secret, m Meta) error {
	cred, err := c.Vault.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := validateSecret(cred.Kind, secret); err != nil {
		return err
	}
	if err := c.Vault.UpdateSecret(ctx, id, secret); err != nil {
		return err
	}
	c.Audit.Write(ctx, m.event("credential.rotate", "credential", id, nil, nil))
	return nil
}

func (c *CredentialService) Delete(ctx context.Context, id string, m Meta) error {
	if err := c.Vault.Delete(ctx, id); err != nil {
		return err
	}
	c.Audit.Write(ctx, m.event("credential.delete", "credential", id, nil, nil))
	return nil
}

func (c *CredentialService) Test(ctx context.Context, id, target string, port int, m Meta) (TestResult, error) {
	cred, err := c.Vault.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	secret, err := c.Vault.Decrypt(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	if port == 0 {
		port = 161
	}
	res := c.Tester.Test(ctx, target, port, cred.Kind, secret)
	c.Audit.Write(ctx, m.event("credential.test", "credential", id, nil,
		map[string]any{"target": target, "result": res.Result}))
	return res, nil
}
