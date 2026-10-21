// Package events — SNMP trap intake (ADR-025, doc 36).
//
// netinv-events receives SNMP v1 and v2c traps and informs, resolves each to
// the device that sent it, stores it in events.snmp_traps, and publishes one
// netinv_snmp_trap sample per trap onto metrics.raw. The sample is what makes a
// trap alertable: the alert engine evaluates rules over VictoriaMetrics, so a
// trap that is a sample can drive an ordinary rule, inherit silences and
// notification routing, and link to its device and interface the way every
// other alert does — with no second alerting path to keep in step.
//
// SNMPv3 traps are not decoded. They need a per-device user and engine ID
// table, which is a credential-management feature in its own right; they are
// counted as unsupported rather than silently dropped (doc 36 §2).
package events

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gosnmp/gosnmp"
)

// Bounds on what one trap may carry into storage. A trap is attacker-
// influenceable input on an unauthenticated UDP port: without these, one
// datagram could write an unbounded row.
const (
	MaxVarbinds   = 64
	MaxValueBytes = 512
)

// Varbind is one decoded variable binding, rendered for display. Values are
// strings because the event store exists for an operator to read, not for
// arithmetic; the numeric path is the metric.
type Varbind struct {
	OID   string `json:"oid"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Trap is a decoded notification.
type Trap struct {
	At       time.Time
	Source   netip.Addr // UDP source address
	Agent    netip.Addr // v1 agent-addr, when the PDU carries one
	Version  string     // "1" | "2c"
	Inform   bool
	TrapOID  string // numeric, no leading dot
	Name     string // symbolic name when known, else ""
	IfIndex  string // from an IF-MIB varbind, when the trap is about a port
	Varbinds []Varbind
	DeviceID string // set by the resolver; empty for an unknown source

	packet *gosnmp.SnmpPacket // retained to acknowledge an inform
}

// Label is what the trap is called in the metric and in alert summaries: the
// symbolic name where one is known, otherwise the numeric OID.
func (t *Trap) Label() string {
	if t.Name != "" {
		return t.Name
	}
	return t.TrapOID
}

var (
	ErrUnsupportedVersion = errors.New("snmp version not supported")
	ErrNotATrap           = errors.New("pdu is not a trap or inform")
)

const (
	oidSnmpTrapOID   = "1.3.6.1.6.3.1.1.4.1.0"
	oidSysUpTime     = "1.3.6.1.2.1.1.3.0"
	oidIfTableColumn = "1.3.6.1.2.1.2.2.1." // ifEntry.<column>.<ifIndex>
	oidSnmpTraps     = "1.3.6.1.6.3.1.1.5." // SNMPv2-MIB snmpTraps
)

var quiet = gosnmp.NewLogger(log.New(io.Discard, "", 0))

// PeekVersion reads the SNMP version from a message header without decoding
// the rest, so a v3 trap can be counted as unsupported rather than as noise.
// It returns -1 for anything that is not a BER SEQUENCE starting with an
// INTEGER.
func PeekVersion(b []byte) int {
	if len(b) < 2 || b[0] != 0x30 {
		return -1
	}
	i := 2
	if b[1]&0x80 != 0 { // long-form length
		i += int(b[1] & 0x7f)
	}
	if len(b) < i+3 || b[i] != 0x02 || b[i+1] != 0x01 {
		return -1
	}
	return int(b[i+2])
}

// Decode parses one datagram as a v1 or v2c trap or inform.
func Decode(b []byte, src netip.Addr, at time.Time) (*Trap, string, error) {
	switch PeekVersion(b) {
	case int(gosnmp.Version1), int(gosnmp.Version2c):
	case int(gosnmp.Version3):
		return nil, "", ErrUnsupportedVersion
	default:
		return nil, "", fmt.Errorf("not an SNMP message")
	}
	g := &gosnmp.GoSNMP{Logger: quiet}
	p, err := g.UnmarshalTrap(b, false)
	if err != nil {
		return nil, "", err
	}
	t := &Trap{At: at, Source: src, packet: p}
	switch p.PDUType {
	case gosnmp.Trap:
		t.Version = "1"
		t.TrapOID = v1TrapOID(p.Enterprise, p.GenericTrap, p.SpecificTrap)
		if a, err := netip.ParseAddr(p.AgentAddress); err == nil && !a.IsUnspecified() {
			t.Agent = a
		}
	case gosnmp.SNMPv2Trap, gosnmp.InformRequest:
		t.Version = "2c"
		t.Inform = p.PDUType == gosnmp.InformRequest
	default:
		return nil, "", ErrNotATrap
	}
	for _, v := range p.Variables {
		name := strings.TrimPrefix(v.Name, ".")
		if t.Version == "2c" {
			switch name {
			case oidSnmpTrapOID:
				if s, ok := v.Value.(string); ok {
					t.TrapOID = strings.TrimPrefix(s, ".")
				}
				continue
			case oidSysUpTime:
				continue // the agent's uptime, not something an operator reads off a trap
			}
		}
		if t.IfIndex == "" && strings.HasPrefix(name, oidIfTableColumn) {
			if i := strings.LastIndexByte(name, '.'); i > 0 {
				t.IfIndex = name[i+1:]
			}
		}
		if len(t.Varbinds) < MaxVarbinds {
			t.Varbinds = append(t.Varbinds, render(name, v))
		}
	}
	if t.TrapOID == "" {
		return nil, "", fmt.Errorf("v2c trap without snmpTrapOID.0")
	}
	t.Name = TrapName(t.TrapOID)
	return t, p.Community, nil
}

// Ack builds the response that acknowledges an inform, or nil for a trap. An
// unacknowledged inform is retransmitted by the agent, so a listener that
// stored it and stayed silent would store it again on every retry.
func (t *Trap) Ack() ([]byte, error) {
	if !t.Inform || t.packet == nil {
		return nil, nil
	}
	resp := *t.packet
	resp.PDUType = gosnmp.GetResponse
	resp.Error = gosnmp.NoError
	resp.ErrorIndex = 0
	return resp.MarshalMsg()
}

// v1TrapOID maps a v1 trap onto the v2 snmpTrapOID it corresponds to, per RFC
// 3584 §3.1, so v1 and v2c traps of the same kind share one label and one rule.
func v1TrapOID(enterprise string, generic, specific int) string {
	enterprise = strings.TrimPrefix(enterprise, ".")
	if generic >= 0 && generic < 6 {
		return oidSnmpTraps + strconv.Itoa(generic+1)
	}
	return enterprise + ".0." + strconv.Itoa(specific)
}

func render(name string, v gosnmp.SnmpPDU) Varbind {
	vb := Varbind{OID: name, Type: v.Type.String()}
	switch x := v.Value.(type) {
	case []byte:
		if utf8.Valid(x) && printable(x) {
			vb.Value = string(x)
		} else {
			vb.Value = hexString(x)
		}
	case string:
		vb.Value = strings.TrimPrefix(x, ".")
	case nil:
		vb.Value = ""
	default: // integers, counters, gauges, timeticks
		vb.Value = fmt.Sprint(x)
	}
	if len(vb.Value) > MaxValueBytes {
		vb.Value = vb.Value[:MaxValueBytes] + "…"
	}
	return vb
}

func printable(b []byte) bool {
	for _, c := range string(b) {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}

func hexString(b []byte) string {
	const digits = "0123456789ABCDEF"
	var sb strings.Builder
	for i, c := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteByte(digits[c>>4])
		sb.WriteByte(digits[c&0x0f])
	}
	return sb.String()
}
