package maps

import "testing"

// LLDP reports an adjacency from both ends, and each row carries the ifIndex
// of its own end only. A generator that treats the two rows as two links draws
// the topology twice; one that keeps only the first binds one side and leaves
// the other graphing nothing. Both halves have to merge into a single link
// holding both ifIndexes.
func TestBuildCollapsesBothDirectionsIntoOneBoundLink(t *testing.T) {
	def := BuildTopologyDefinition("t", []Suggestion{
		{ADeviceID: "d_b", ADevice: "core-b", AIfIndex: 7, BDeviceID: "d_a", BDevice: "core-a"},
		{ADeviceID: "d_a", ADevice: "core-a", AIfIndex: 3, BDeviceID: "d_b", BDevice: "core-b"},
	})
	if len(def.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2: %+v", len(def.Nodes), def.Nodes)
	}
	if len(def.Links) != 1 {
		t.Fatalf("got %d links, want the two directions collapsed into 1", len(def.Links))
	}
	l := def.Links[0]
	if l.AEndpoint == nil || l.BEndpoint == nil {
		t.Fatalf("link is half-bound: %+v", l)
	}
	// Ends are keyed by device id, not by which row arrived first.
	got := map[string]int{
		l.AEndpoint.DeviceID: l.AEndpoint.IfIndex,
		l.BEndpoint.DeviceID: l.BEndpoint.IfIndex,
	}
	if got["d_a"] != 3 || got["d_b"] != 7 {
		t.Fatalf("endpoints bound to %v, want d_a=3 d_b=7", got)
	}
}

// Only one direction seen: the known end is bound and the other is left
// unbound rather than guessed. A wrongly bound endpoint graphs a real port
// that is not the link, which is harder to spot than one that graphs nothing.
func TestBuildLeavesUnknownEndpointUnbound(t *testing.T) {
	def := BuildTopologyDefinition("t", []Suggestion{
		{ADeviceID: "d_a", ADevice: "core-a", AIfIndex: 3, BDeviceID: "d_b", BDevice: "core-b"},
	})
	if len(def.Links) != 1 {
		t.Fatalf("got %d links, want 1", len(def.Links))
	}
	l := def.Links[0]
	bound, unbound := l.AEndpoint, l.BEndpoint
	if bound == nil {
		bound, unbound = l.BEndpoint, l.AEndpoint
	}
	if bound == nil || bound.DeviceID != "d_a" || bound.IfIndex != 3 {
		t.Fatalf("known end not bound: %+v", l)
	}
	if unbound != nil {
		t.Fatalf("unknown end was bound to %+v — that is a guess", unbound)
	}
}

// An LLDP neighbour NetInv does not manage has no interfaces to graph. A node
// for it would be a link that is permanently idle, which reads as a fault.
func TestBuildSkipsUnmanagedNeighboursAndSelfLinks(t *testing.T) {
	def := BuildTopologyDefinition("t", []Suggestion{
		{ADeviceID: "d_a", ADevice: "core-a", AIfIndex: 1, BDeviceID: "", BSysName: "some-switch"},
		{ADeviceID: "d_a", ADevice: "core-a", AIfIndex: 2, BDeviceID: "d_a", BDevice: "core-a"},
	})
	if len(def.Nodes) != 0 || len(def.Links) != 0 {
		t.Fatalf("got %d nodes / %d links, want nothing drawable",
			len(def.Nodes), len(def.Links))
	}
}

// Regenerating after the estate changes is normal. Unstable ids would make
// every diff look like everything moved.
func TestBuildIsDeterministic(t *testing.T) {
	in := []Suggestion{
		{ADeviceID: "d_c", ADevice: "c", AIfIndex: 1, BDeviceID: "d_a", BDevice: "a"},
		{ADeviceID: "d_a", ADevice: "a", AIfIndex: 2, BDeviceID: "d_b", BDevice: "b"},
		{ADeviceID: "d_b", ADevice: "b", AIfIndex: 3, BDeviceID: "d_c", BDevice: "c"},
	}
	first := BuildTopologyDefinition("t", in)
	// Reversed input: the same estate, reported in a different order.
	rev := []Suggestion{in[2], in[0], in[1]}
	second := BuildTopologyDefinition("t", rev)

	if len(first.Nodes) != 3 || len(first.Links) != 3 {
		t.Fatalf("triangle produced %d nodes / %d links", len(first.Nodes), len(first.Links))
	}
	for i := range first.Nodes {
		a, b := first.Nodes[i], second.Nodes[i]
		if a.ID != b.ID || a.DeviceID != b.DeviceID || a.X != b.X || a.Y != b.Y {
			t.Fatalf("node %d differs between runs: %+v vs %+v", i, a, b)
		}
	}
	for i := range first.Links {
		if first.Links[i].ID != second.Links[i].ID ||
			first.Links[i].From != second.Links[i].From ||
			first.Links[i].To != second.Links[i].To {
			t.Fatalf("link %d differs between runs: %+v vs %+v",
				i, first.Links[i], second.Links[i])
		}
	}
}

// The document has to survive the same validation a hand-drawn one does, or
// the generator produces maps the editor refuses to save.
func TestGeneratedDefinitionValidates(t *testing.T) {
	def := BuildTopologyDefinition("t", []Suggestion{
		{ADeviceID: "d_a", ADevice: "a", AIfIndex: 1, BDeviceID: "d_b", BDevice: "b"},
		{ADeviceID: "d_b", ADevice: "b", AIfIndex: 2, BDeviceID: "d_a", BDevice: "a"},
	})
	if err := def.Validate(); err != nil {
		t.Fatalf("generated document is invalid: %v", err)
	}
}

// Nodes must not land on top of each other; a map where every device shares a
// coordinate is not a starting point for anything.
func TestLayoutSeparatesNodes(t *testing.T) {
	for _, n := range []int{2, 5, 16, 17, 40} {
		seen := map[[2]float64]bool{}
		for i := range n {
			x, y := layoutPosition(i, n)
			if seen[[2]float64{x, y}] {
				t.Fatalf("n=%d: node %d overlaps an earlier one at %v,%v", n, i, x, y)
			}
			seen[[2]float64{x, y}] = true
		}
	}
}
