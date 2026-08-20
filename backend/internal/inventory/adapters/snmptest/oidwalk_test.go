package snmptest

import "testing"

// String comparison of OIDs is wrong in a way that looks right until it bites:
// ".1.3.6.1.2.1.10" sorts before ".1.3.6.1.2.1.9" as text. The walk uses this
// comparison to detect an agent that has stopped advancing, so getting it wrong
// would abandon a walk in the middle of the interfaces tree and — before the
// completeness fix — report the result as the whole subtree.
func TestCompareOIDIsNumericNotLexical(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{".1.3.6.1.2.1.9", ".1.3.6.1.2.1.10", -1}, // 9 < 10, though "9" > "1" as text
		{".1.3.6.1.2.1.10", ".1.3.6.1.2.1.9", 1},
		{".1.3.6.1.2.1.2", ".1.3.6.1.2.1.2", 0},
		{".1.3.6.1.2.1.2", ".1.3.6.1.2.1.2.1", -1}, // a prefix sorts first
		{".1.3.6.1.2.1.2.1", ".1.3.6.1.2.1.2", 1},
		{"1.3.6.1", ".1.3.6.1", 0}, // leading dot is not significant
	}
	for _, c := range cases {
		if got := compareOID(c.a, c.b); got != c.want {
			t.Errorf("compareOID(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// The trailing-dot check is what stops a walk of .1.3 from wandering into .1.30
// and never deciding it has finished.
func TestUnderRootDoesNotMatchSiblingPrefixes(t *testing.T) {
	cases := []struct {
		oid, root string
		want      bool
	}{
		{".1.3.6.1.2.1.1.1.0", ".1.3", true},
		{".1.3", ".1.3", true},
		{".1.30.1", ".1.3", false}, // the bug a naive HasPrefix would introduce
		{".1.3.6.1.4.1.9", ".1.3", true},
		{".1.0.8802.1.1.2", ".1.0", true},
		{".1.3.6.1.2.1", ".1.3.6.1.4.1", false},
		{"1.3.6.1.2.1.1", ".1.3.6.1.2.1", true}, // mixed leading dots
	}
	for _, c := range cases {
		if got := underRoot(c.oid, c.root); got != c.want {
			t.Errorf("underRoot(%s, %s) = %v, want %v", c.oid, c.root, got, c.want)
		}
	}
}

// A non-numeric arc must not silently compare as equal, or a malformed OID from
// an agent would read as "not increasing" and stop an otherwise healthy walk in
// a way nobody could explain.
func TestCompareOIDHandlesMalformed(t *testing.T) {
	if compareOID(".1.3.6.x", ".1.3.6.x") != 0 {
		t.Error("identical malformed OIDs should compare equal")
	}
	if compareOID(".1.3.6.a", ".1.3.6.b") >= 0 {
		t.Error("malformed OIDs should fall back to a stable string order")
	}
}
