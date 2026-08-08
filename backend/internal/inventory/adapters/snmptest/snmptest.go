// Package snmptest implements the credential test probe (FR-CRED-03).
// v1 note: the probe runs from the API process; at sites unreachable from the
// core, testing happens implicitly at first poll (doc 11 §6 auth-failure path).
package snmptest

import (
	"context"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	invapp "github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
)

const sysNameOID = ".1.3.6.1.2.1.1.5.0"

type Tester struct{}

func (Tester) Test(ctx context.Context, target string, port int,
	kind domain.CredentialKind, secret domain.Secret) invapp.TestResult {

	g := newClient(ctx, target, port, kind, secret, 3*time.Second)

	start := time.Now()
	if err := g.Connect(); err != nil {
		return invapp.TestResult{Result: "error", Detail: "socket: " + err.Error()}
	}
	defer g.Conn.Close()

	pkt, err := g.Get([]string{sysNameOID})
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return classify(err, latency)
	}
	if pkt.Error != gosnmp.NoError || len(pkt.Variables) == 0 {
		return invapp.TestResult{Result: "error", LatencyMS: latency,
			Detail: "SNMP error status " + pkt.Error.String()}
	}
	name := ""
	if b, ok := pkt.Variables[0].Value.([]byte); ok {
		name = string(b)
	}
	return invapp.TestResult{Result: "ok", SysName: name, LatencyMS: latency}
}

func classify(err error, latency int64) invapp.TestResult {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timeout"):
		// v2c wrong community and dead host are indistinguishable on the wire:
		// agents silently drop bad communities. Reported as timeout by design.
		return invapp.TestResult{Result: "timeout", LatencyMS: latency}
	case strings.Contains(msg, "authentication"), strings.Contains(msg, "unknown username"),
		strings.Contains(msg, "wrong digest"):
		return invapp.TestResult{Result: "auth_failure", LatencyMS: latency}
	case strings.Contains(msg, "decryption"), strings.Contains(msg, "privacy"):
		return invapp.TestResult{Result: "priv_failure", LatencyMS: latency}
	default:
		return invapp.TestResult{Result: "error", LatencyMS: latency, Detail: msg}
	}
}

// newClient builds a gosnmp client for a credential (shared by Test and Walk).
func newClient(ctx context.Context, target string, port int,
	kind domain.CredentialKind, secret domain.Secret, timeout time.Duration) *gosnmp.GoSNMP {
	g := &gosnmp.GoSNMP{
		Target: target, Port: uint16(port), Timeout: timeout, Retries: 1,
		MaxRepetitions: 25, Context: ctx,
	}
	switch kind {
	case domain.SNMPv2c:
		g.Version = gosnmp.Version2c
		g.Community = secret.Community
	case domain.SNMPv3:
		g.Version = gosnmp.Version3
		g.SecurityModel = gosnmp.UserSecurityModel
		usm := &gosnmp.UsmSecurityParameters{
			UserName:                 secret.Username,
			AuthenticationPassphrase: secret.AuthPassword,
			PrivacyPassphrase:        secret.PrivPassword,
		}
		switch secret.AuthProtocol {
		case "sha256":
			usm.AuthenticationProtocol = gosnmp.SHA256
		case "sha1":
			usm.AuthenticationProtocol = gosnmp.SHA
		case "md5":
			usm.AuthenticationProtocol = gosnmp.MD5
		}
		g.MsgFlags = gosnmp.AuthNoPriv
		switch secret.PrivProtocol {
		case "aes256":
			usm.PrivacyProtocol = gosnmp.AES256
			g.MsgFlags = gosnmp.AuthPriv
		case "aes128":
			usm.PrivacyProtocol = gosnmp.AES
			g.MsgFlags = gosnmp.AuthPriv
		case "des":
			usm.PrivacyProtocol = gosnmp.DES
			g.MsgFlags = gosnmp.AuthPriv
		}
		g.SecurityParameters = usm
		g.ContextName = secret.Context
	}
	return g
}
