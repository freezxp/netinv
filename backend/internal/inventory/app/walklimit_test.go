package app

import "testing"

// The walk limit had a defect worth pinning: any request above the old ceiling
// was rewritten down to 1000, while the handler measured "was this truncated?"
// against the number the caller asked for. A walk capped at 1000 objects
// therefore reported itself complete.
//
// That matters more than an ordinary off-by-one because of what the result is
// for. An OID dump is read somewhere else — attached to an issue, handed over
// for connector work — by someone who cannot re-run the walk and has no way to
// tell a device that stops at 1000 objects from a file that does.
func TestClampWalkLimitNeverSilentlyShrinks(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero means default", 0, DefaultWalkObjects},
		{"negative means default", -5, DefaultWalkObjects},
		{"small request honoured", 250, 250},
		{"the old ceiling is not special", 5001, 5001},
		{"a full-tree request is honoured", 200000, 200000},
		{"beyond the ceiling clamps to the ceiling", MaxWalkObjects + 1, MaxWalkObjects},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClampWalkLimit(c.in); got != c.want {
				t.Errorf("ClampWalkLimit(%d) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// The specific shape of the old bug: a large request must never come back as
// something smaller than a modest one. If it does, a caller asking for
// everything gets less than a caller asking for a page.
func TestClampWalkLimitIsMonotonic(t *testing.T) {
	prev := ClampWalkLimit(1)
	for _, n := range []int{10, 999, 1000, 1001, 5000, 5001, 20000, MaxWalkObjects} {
		got := ClampWalkLimit(n)
		if got < prev {
			t.Fatalf("ClampWalkLimit(%d) = %d, which is less than the previous %d — "+
				"a bigger request must not return fewer objects", n, got, prev)
		}
		prev = got
	}
}
