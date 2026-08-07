// Package domain — Inventory context entities (doc 16). Sprint 4 scope:
// Site and Credential; Device/Interface arrive in Sprint 5.
package domain

import "time"

type Site struct {
	ID           string
	TenantID     string
	Name         string
	ParentSiteID *string
	Location     string
	Contact      string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type CredentialKind string

const (
	SNMPv2c CredentialKind = "snmp_v2c"
	SNMPv3  CredentialKind = "snmp_v3"
)

// Credential is the stored (sealed) form; secret material never appears here.
type Credential struct {
	ID          string
	TenantID    string
	Name        string
	Kind        CredentialKind
	Meta        map[string]any // non-secret display fields (doc 08)
	DeviceCount int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Secret is the plaintext form, present only in create/update requests and
// inside pollers at use time (doc 20 §6).
type Secret struct {
	// v2c
	Community string `json:"community,omitempty"`
	// v3
	Username     string `json:"username,omitempty"`
	AuthProtocol string `json:"auth_protocol,omitempty"` // sha1|sha256|md5(deprecated)
	AuthPassword string `json:"auth_password,omitempty"`
	PrivProtocol string `json:"priv_protocol,omitempty"` // aes128|aes256|des(deprecated)
	PrivPassword string `json:"priv_password,omitempty"`
	Context      string `json:"context,omitempty"`
}

// PublicMeta returns the displayable, non-secret subset (FR-CRED-01).
func (s Secret) PublicMeta(kind CredentialKind) map[string]any {
	if kind == SNMPv2c {
		return map[string]any{}
	}
	return map[string]any{
		"username":      s.Username,
		"auth_protocol": s.AuthProtocol,
		"priv_protocol": s.PrivProtocol,
	}
}
