package sdk

import "testing"

// The obvious spelling of "base plus health" duplicated whatever the base
// already declared, and the base does declare health — six connectors reported
// it twice and the catalogue rendered a doubled badge.
func TestAddCapsDoesNotDuplicateWhatTheBaseAlreadyHas(t *testing.T) {
	base := []Capability{CapInventory, CapInterfaces, CapTopology, CapHealth}
	got := AddCaps(base, CapHealth)
	if len(got) != len(base) {
		t.Fatalf("got %v, want no change when the capability is already present", got)
	}
	seen := map[Capability]int{}
	for _, c := range got {
		seen[c]++
	}
	for c, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times", c, n)
		}
	}
}

func TestAddCapsAppendsWhatIsMissing(t *testing.T) {
	got := AddCaps([]Capability{CapInventory}, CapHealth, CapTopology)
	if len(got) != 3 {
		t.Fatalf("got %v, want three capabilities", got)
	}
	if got[0] != CapInventory {
		t.Errorf("the base's order was not preserved: %v", got)
	}
}

// The base slice belongs to its caller; appending to a copy is what keeps a
// second connector from seeing the first one's additions.
func TestAddCapsDoesNotMutateTheBase(t *testing.T) {
	base := make([]Capability, 1, 8) // spare capacity: append would write in place
	base[0] = CapInventory
	_ = AddCaps(base, CapHealth)
	if len(base) != 1 || base[0] != CapInventory {
		t.Errorf("base was mutated to %v", base)
	}
}
