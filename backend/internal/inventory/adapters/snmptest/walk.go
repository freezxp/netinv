package snmptest

import (
	"context"
	"fmt"
	"strings"
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
) (invapp.WalkResult, error) {
	// Defensive only: the caller (invapp.ClampWalkLimit) owns the policy. This
	// must never rewrite a large limit down to a small one — doing so produced
	// a truncated walk that the handler then reported as complete.
	limit = invapp.ClampWalkLimit(limit)
	if strings.TrimSpace(root) == "" {
		root = ".1.3.6.1.2.1"
	}
	g := newClient(ctx, target, port, kind, secret, 5*time.Second)
	if err := g.Connect(); err != nil {
		return invapp.WalkResult{}, errx.Wrap(errx.KindTransient, err, "snmp connect")
	}
	defer g.Conn.Close()

	// A hand-rolled GETBULK loop rather than gosnmp's BulkWalk, for one reason:
	// BulkWalk reports that it finished and gives no way to tell "walked the
	// subtree to its end" from "the agent stopped talking half way down". Both
	// look like success. This loop knows which happened, because it sees the
	// varbind that ends the walk.
	out := make([]invapp.OIDValue, 0, 256)
	next := root
	for {
		if len(out) >= limit {
			return invapp.WalkResult{Values: out, Stopped: fmt.Sprintf(
				"reached the %d-object ceiling", limit)}, nil
		}
		resp, err := g.GetBulk([]string{next}, 0, uint32(g.MaxRepetitions))
		if err != nil {
			if len(out) == 0 {
				return invapp.WalkResult{}, errx.Wrap(errx.KindTransient, err, "snmp walk")
			}
			// Partial data is worth returning — it is still a dump, as long as
			// nobody is told it is a whole one.
			return invapp.WalkResult{Values: out, Stopped: "agent stopped responding: " +
				err.Error()}, nil
		}
		if len(resp.Variables) == 0 {
			return invapp.WalkResult{Values: out,
				Stopped: "agent returned an empty response before the end of the subtree"}, nil
		}
		for _, v := range resp.Variables {
			switch v.Type {
			case gosnmp.EndOfMibView:
				// Nothing further exists anywhere on the agent, so the subtree
				// is finished by definition.
				return invapp.WalkResult{Values: out, Complete: true}, nil
			case gosnmp.NoSuchObject, gosnmp.NoSuchInstance:
				return invapp.WalkResult{Values: out, Complete: true}, nil
			}
			if !underRoot(v.Name, root) {
				// Walked past the far edge of the subtree: the only genuinely
				// complete outcome.
				return invapp.WalkResult{Values: out, Complete: true}, nil
			}
			if compareOID(v.Name, next) <= 0 && len(out) > 0 {
				// A conformant agent always answers GETBULK with a greater OID.
				// One that does not would loop this walk forever.
				return invapp.WalkResult{Values: out, Stopped: fmt.Sprintf(
					"agent returned a non-increasing OID (%s after %s)", v.Name, next)}, nil
			}
			out = append(out, invapp.OIDValue{
				OID:   v.Name,
				Type:  snmpTypeName(v.Type),
				Value: renderValue(v),
			})
			next = v.Name
			if len(out) >= limit {
				break
			}
		}
	}
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
