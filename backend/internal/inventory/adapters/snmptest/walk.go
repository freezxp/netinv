package snmptest

import (
	"context"
	"fmt"
	"time"

	"github.com/gosnmp/gosnmp"

	invapp "github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// Walk performs a live SNMP walk for the OID browser (doc 30 §5). Like the
// credential test (FR-CRED-03) this runs from the API process, so it can only
// reach devices the core can reach; devices behind a remote poller return a
// timeout. Dispatching the walk to the owning poller is the follow-up that
// makes it work everywhere.
func (Tester) Walk(ctx context.Context, target string, port int,
	kind domain.CredentialKind, secret domain.Secret, root string, limit int,
) ([]invapp.OIDValue, error) {
	// Defensive only: the caller (invapp.ClampWalkLimit) owns the policy. This
	// must never rewrite a large limit down to a small one — doing so produced
	// a truncated walk that the handler then reported as complete.
	limit = invapp.ClampWalkLimit(limit)
	g := newClient(ctx, target, port, kind, secret, 5*time.Second)
	if err := g.Connect(); err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "snmp connect")
	}
	defer g.Conn.Close()

	out := make([]invapp.OIDValue, 0, 64)
	err := g.BulkWalk(root, func(p gosnmp.SnmpPDU) error {
		if len(out) >= limit {
			return fmt.Errorf("limit reached")
		}
		out = append(out, invapp.OIDValue{
			OID:   p.Name,
			Type:  snmpTypeName(p.Type),
			Value: renderValue(p),
		})
		return nil
	})
	// Hitting the cap is a normal stop, not a failure.
	if err != nil && len(out) < limit {
		return out, errx.Wrap(errx.KindTransient, err, "snmp walk")
	}
	return out, nil
}

func snmpTypeName(t gosnmp.Asn1BER) string {
	switch t {
	case gosnmp.OctetString:
		return "OctetString"
	case gosnmp.Integer:
		return "Integer"
	case gosnmp.Counter32:
		return "Counter32"
	case gosnmp.Counter64:
		return "Counter64"
	case gosnmp.Gauge32:
		return "Gauge32"
	case gosnmp.TimeTicks:
		return "TimeTicks"
	case gosnmp.ObjectIdentifier:
		return "OID"
	case gosnmp.IPAddress:
		return "IPAddress"
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance:
		return "NoSuchObject"
	default:
		return fmt.Sprintf("0x%X", byte(t))
	}
}

// renderValue prints octet strings readably, falling back to hex for binary
// (MAC addresses and similar) so the browser never shows control characters.
func renderValue(p gosnmp.SnmpPDU) string {
	switch x := p.Value.(type) {
	case []byte:
		printable := true
		for _, b := range x {
			if b < 32 || b > 126 {
				printable = false
				break
			}
		}
		if printable {
			return string(x)
		}
		hex := make([]byte, 0, len(x)*3)
		const digits = "0123456789abcdef"
		for i, b := range x {
			if i > 0 {
				hex = append(hex, ':')
			}
			hex = append(hex, digits[b>>4], digits[b&0x0f])
		}
		return string(hex)
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}
