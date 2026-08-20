package snmptest

import (
	"strconv"
	"strings"
)

// OID ordering and subtree tests, kept separate from the transport so they can
// be tested without a device.
//
// These exist because "the walk returned data and no error" is not the same as
// "the subtree was walked to its end", and the difference is not cosmetic: a
// dump is used to conclude that a device does NOT implement something, and that
// inference is only sound when the walk actually finished. A real export of a
// Cisco ASR 900 stopped part-way through .1.3.6.1.2.1.10 and was reported
// complete, which led to the confident and wrong conclusion that the device
// exposed no CISCO-PROCESS-MIB — it had simply never been reached.

// arcs splits a dotted OID into its numeric components. A component that is not
// a number makes the OID unusable for ordering, which the caller treats as a
// reason to stop rather than risk a wrong comparison.
func arcs(oid string) ([]int, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(oid), ".")
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// compareOID orders two OIDs the way SNMP does: component by component, as
// numbers. String comparison is wrong and dangerously plausible — it puts
// ".1.3.6.1.2.1.10" before ".1.3.6.1.2.1.9", which would make a walk look like
// it had gone backwards and stop it in the middle of the interfaces tree.
func compareOID(a, b string) int {
	x, okA := arcs(a)
	y, okB := arcs(b)
	if !okA || !okB {
		return strings.Compare(a, b)
	}
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(x) < len(y):
		return -1
	case len(x) > len(y):
		return 1
	}
	return 0
}

// underRoot reports whether oid lies within the root subtree.
//
// The trailing dot matters: a plain string prefix test puts ".1.30" inside
// ".1.3", so a walk of .1.3 would keep collecting objects from a sibling
// subtree and never report itself finished.
func underRoot(oid, root string) bool {
	o := strings.TrimPrefix(strings.TrimSpace(oid), ".")
	r := strings.TrimPrefix(strings.TrimSpace(root), ".")
	if r == "" {
		return o != ""
	}
	return o == r || strings.HasPrefix(o, r+".")
}
