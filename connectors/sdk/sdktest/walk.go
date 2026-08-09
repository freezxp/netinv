// Package sdktest loads recorded snmpwalk fixtures as a Session, so connector
// tests run against what a real agent actually returned rather than against a
// hand-written map of what someone believed it returns.
//
// The difference is not academic. Every correction the validated connectors
// needed came from a device disagreeing with its own MIB: a Ruckus R710
// reporting speed in ifSpeed while leaving ifHighSpeed at 0, a UniFi console
// answering with a net-snmp sysObjectID. A hand-written fixture encodes the
// belief; a recorded one encodes the device.
//
// Fixtures live in each connector's testdata/ and are produced by
// scripts/record-fixture.sh, which redacts identity at capture time.
package sdktest

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
)

// Session is a read-only sdk.Session backed by a recorded walk.
type Session struct {
	vars  []sdk.Var
	byOID map[string]sdk.Var
	meta  sdk.TargetMeta
}

// Load reads a fixture and fails the test if it is missing, malformed, or
// empty. Empty is treated as an error deliberately: a fixture that parses to
// nothing makes every assertion against it vacuously pass.
func Load(t *testing.T, path string) *Session {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("sdktest: %v", err)
	}
	defer f.Close()

	s := &Session{byOID: map[string]sdk.Var{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		oid, value, ok := parse(text)
		if !ok {
			t.Fatalf("sdktest: %s:%d: cannot parse %q", path, line, text)
		}
		v := sdk.Var{OID: oid, Value: value}
		s.vars = append(s.vars, v)
		s.byOID[oid] = v
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("sdktest: reading %s: %v", path, err)
	}
	if len(s.vars) == 0 {
		t.Fatalf("sdktest: %s contains no varbinds", path)
	}
	return s
}

// parse splits one `OID = TYPE: VALUE` line into an OID and a Go value of the
// same type gosnmp would produce, so a connector cannot pass against a fixture
// and fail against the wire purely on type assertions.
func parse(line string) (oid string, value any, ok bool) {
	eq := strings.Index(line, " = ")
	if eq < 0 {
		return "", nil, false
	}
	oid = strings.TrimSpace(line[:eq])
	rest := strings.TrimSpace(line[eq+3:])
	if !strings.HasPrefix(oid, ".") {
		oid = "." + oid
	}

	kind, payload := "", rest
	if c := strings.Index(rest, ": "); c >= 0 {
		if k := rest[:c]; !strings.Contains(k, " ") || k == "Hex-STRING" || k == "Network Address" {
			kind, payload = k, strings.TrimSpace(rest[c+2:])
		}
	}

	switch kind {
	case "STRING":
		// gosnmp hands octet strings back as []byte.
		return oid, []byte(strings.Trim(payload, `"`)), true
	case "Hex-STRING":
		return oid, hexBytes(payload), true
	case "OID", "IpAddress", "Network Address":
		return oid, payload, true
	case "Counter64":
		n, err := strconv.ParseUint(payload, 10, 64)
		return oid, n, err == nil
	case "Counter32", "Gauge32", "UInteger32":
		n, err := strconv.ParseUint(payload, 10, 32)
		return oid, uint(n), err == nil
	case "INTEGER":
		// May be rendered as `up(1)` when a MIB is loaded; -On -Ot avoids that,
		// but tolerate it so a hand-trimmed fixture still loads.
		if p := strings.Index(payload, "("); p >= 0 && strings.HasSuffix(payload, ")") {
			payload = payload[p+1 : len(payload)-1]
		}
		n, err := strconv.Atoi(payload)
		return oid, n, err == nil
	case "":
		// Bare numbers: timeticks under -Ot, and anything net-snmp prints
		// without a type tag.
		if n, err := strconv.ParseUint(payload, 10, 64); err == nil {
			return oid, n, true
		}
		return oid, []byte(strings.Trim(payload, `"`)), true
	default:
		return oid, []byte(payload), true
	}
}

func hexBytes(s string) []byte {
	out := make([]byte, 0, 8)
	for _, f := range strings.Fields(s) {
		n, err := strconv.ParseUint(f, 16, 8)
		if err != nil {
			return out
		}
		out = append(out, byte(n))
	}
	return out
}

func (s *Session) Get(_ context.Context, oids []string) ([]sdk.Var, error) {
	out := make([]sdk.Var, 0, len(oids))
	for _, oid := range oids {
		if !strings.HasPrefix(oid, ".") {
			oid = "." + oid
		}
		if v, ok := s.byOID[oid]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// Walk returns every varbind under root, in recorded order — which is lexical
// OID order, the same order an agent walks.
func (s *Session) Walk(_ context.Context, root string) ([]sdk.Var, error) {
	if !strings.HasPrefix(root, ".") {
		root = "." + root
	}
	var out []sdk.Var
	for _, v := range s.vars {
		if strings.HasPrefix(v.OID, root+".") || v.OID == root {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *Session) Target() sdk.TargetMeta { return s.meta }

// Has reports whether the fixture recorded anything under root. Tests use it to
// assert an absence the device is known for — the Ruckus R710 exposing no
// CPU or temperature anywhere is a documented property worth pinning.
func (s *Session) Has(root string) bool {
	vs, _ := s.Walk(context.Background(), root)
	return len(vs) > 0
}
