package pollerrt

import (
	"context"
	"fmt"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/freezxp/netinv/connectors/sdk"

	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// SNMPSession implements sdk.Session over gosnmp (doc 10 §2).
type SNMPSession struct {
	g    *gosnmp.GoSNMP
	meta sdk.TargetMeta
}

func NewSNMPSession(job wire.PollJob) (*SNMPSession, error) {
	timeout := time.Duration(job.TimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	// The API rejects out-of-range ports on the way in, but this runs against
	// whatever is already in the database — including rows written before that
	// check existed. Narrowing to uint16 without a guard turns port 99999 into
	// 33999 and polls a port nobody configured, which surfaces as a device that
	// times out for no visible reason. Failing loudly is the cheaper outcome.
	if job.Port < 1 || job.Port > 65535 {
		return nil, fmt.Errorf("snmp port %d is out of range for device %s", job.Port, job.DeviceID)
	}
	g := &gosnmp.GoSNMP{
		Target:             job.MgmtIP,
		Port:               uint16(job.Port),
		Timeout:            timeout,
		Retries:            job.Retries,
		MaxRepetitions:     25,
		ExponentialTimeout: false,
	}
	switch job.Cred.Version {
	case "v2c":
		g.Version = gosnmp.Version2c
		g.Community = job.Cred.Community
	case "v3":
		g.Version = gosnmp.Version3
		g.SecurityModel = gosnmp.UserSecurityModel
		usm := &gosnmp.UsmSecurityParameters{
			UserName:                 job.Cred.Username,
			AuthenticationPassphrase: job.Cred.AuthPass,
			PrivacyPassphrase:        job.Cred.PrivPass,
		}
		switch job.Cred.AuthProto {
		case "sha256":
			usm.AuthenticationProtocol = gosnmp.SHA256
		case "sha1":
			usm.AuthenticationProtocol = gosnmp.SHA
		case "md5":
			usm.AuthenticationProtocol = gosnmp.MD5
		}
		g.MsgFlags = gosnmp.AuthNoPriv
		switch job.Cred.PrivProto {
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
		g.ContextName = job.Cred.Context
	default:
		return nil, fmt.Errorf("pollerrt: unsupported credential version %q", job.Cred.Version)
	}
	if err := g.Connect(); err != nil {
		return nil, fmt.Errorf("pollerrt: connect: %w", err)
	}
	return &SNMPSession{g: g, meta: sdk.TargetMeta{Address: job.MgmtIP, Port: job.Port}}, nil
}

func (s *SNMPSession) Close() {
	if s.g.Conn != nil {
		_ = s.g.Conn.Close()
	}
}

func (s *SNMPSession) Target() sdk.TargetMeta { return s.meta }

func (s *SNMPSession) Get(ctx context.Context, oids []string) ([]sdk.Var, error) {
	s.g.Context = ctx
	pkt, err := s.g.Get(oids)
	if err != nil {
		return nil, err
	}
	out := make([]sdk.Var, 0, len(pkt.Variables))
	for _, v := range pkt.Variables {
		out = append(out, sdk.Var{OID: v.Name, Value: v.Value})
	}
	return out, nil
}

// Walk uses GETBULK (v2c/v3); gosnmp falls back appropriately for v1 agents.
func (s *SNMPSession) Walk(ctx context.Context, root string) ([]sdk.Var, error) {
	s.g.Context = ctx
	var out []sdk.Var
	err := s.g.BulkWalk(root, func(v gosnmp.SnmpPDU) error {
		out = append(out, sdk.Var{OID: v.Name, Value: v.Value})
		return nil
	})
	return out, err
}
