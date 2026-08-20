package maps

import "testing"

var fleet = []DeviceRef{
	{ID: "d_core", Name: "CORE-SW-01", SysName: "core-sw-01.example.invalid"},
	{ID: "d_edge", Name: "EDGE-RTR", SysName: "edge-rtr"},
	{ID: "d_yy", Name: "YY", SysName: "yy-gw"},
	{ID: "d_alpha", Name: "ALPHA", SysName: "alpha"},
}

func find(t *testing.T, out []Suggestion, a, b string) Suggestion {
	t.Helper()
	for _, s := range out {
		if (s.ADeviceID == a && s.BDeviceID == b) || (s.ADeviceID == b && s.BDeviceID == a) {
			return s
		}
	}
	t.Fatalf("no suggestion between %s and %s (got %d)", a, b, len(out))
	return Suggestion{}
}

// Both ends naming each other is the case worth accepting without checking: it
// binds both interfaces and agrees with itself.
func TestBothEndsAgreeBindsBothInterfaces(t *testing.T) {
	out := SuggestFromDescriptions(fleet, []IfaceRef{
		{DeviceID: "d_core", IfIndex: 3, Name: "Gi0/3", Alias: "to EDGE-RTR Gi0/1"},
		{DeviceID: "d_edge", IfIndex: 1, Name: "Gi0/1", Alias: "uplink to CORE-SW-01"},
	})
	s := find(t, out, "d_core", "d_edge")
	if s.Confidence != "both-ends" {
		t.Errorf("confidence = %q, want both-ends", s.Confidence)
	}
	if s.AIfIndex == 0 || s.BIfIndex == 0 {
		t.Errorf("both ifIndexes should be bound, got a=%d b=%d", s.AIfIndex, s.BIfIndex)
	}
	if s.Source != "description" {
		t.Errorf("source = %q", s.Source)
	}
	if s.Evidence == "" {
		t.Error("evidence must carry the text, or the offer cannot be judged")
	}
}

// One-sided evidence is still useful, but must be labelled as such — the far
// end's interface is unknown, so the operator has to pick it.
func TestOneSidedIsOfferedButMarked(t *testing.T) {
	out := SuggestFromDescriptions(fleet, []IfaceRef{
		{DeviceID: "d_core", IfIndex: 4, Name: "Gi0/4", Alias: "link to EDGE-RTR"},
	})
	s := find(t, out, "d_core", "d_edge")
	if s.Confidence != "one-end" {
		t.Errorf("confidence = %q, want one-end", s.Confidence)
	}
	if s.BIfIndex != 0 {
		t.Errorf("far-end ifIndex should be unknown, got %d", s.BIfIndex)
	}
}

// The failure that would make the whole feature untrustworthy: a two-letter
// device name matching inside an unrelated word. "ALPHA" contains "AL", and a
// description mentioning ALPHA must not suggest a link to a device called YY
// or to any short-named box.
func TestShortNamesDoNotMatchInsideWords(t *testing.T) {
	out := SuggestFromDescriptions(fleet, []IfaceRef{
		{DeviceID: "d_core", IfIndex: 5, Name: "Gi0/5", Alias: "customer YYZ handoff"},
	})
	for _, s := range out {
		if s.BDeviceID == "d_yy" || s.ADeviceID == "d_yy" {
			t.Errorf("matched YY inside %q — short names must be token-exact", "YYZ")
		}
	}
}

// ...but a description that is exactly the short name is unmistakable.
func TestShortNameMatchesWhenItIsTheWholeDescription(t *testing.T) {
	out := SuggestFromDescriptions(fleet, []IfaceRef{
		{DeviceID: "d_core", IfIndex: 6, Name: "Gi0/6", Alias: "YY"},
	})
	find(t, out, "d_core", "d_yy")
}

// A port describing its own device is not a link.
func TestSelfReferenceIsNotALink(t *testing.T) {
	out := SuggestFromDescriptions(fleet, []IfaceRef{
		{DeviceID: "d_core", IfIndex: 7, Name: "Gi0/7", Alias: "CORE-SW-01 management"},
	})
	if len(out) != 0 {
		t.Errorf("suggested %d links from a self-reference", len(out))
	}
}

// Several ports naming the same neighbour is a LAG, not several links. One
// suggestion, or the list fills with near-duplicates nobody reads.
func TestParallelPortsCollapseToOneSuggestion(t *testing.T) {
	out := SuggestFromDescriptions(fleet, []IfaceRef{
		{DeviceID: "d_core", IfIndex: 10, Name: "Gi0/10", Alias: "to EDGE-RTR member 1"},
		{DeviceID: "d_core", IfIndex: 11, Name: "Gi0/11", Alias: "to EDGE-RTR member 2"},
	})
	if len(out) != 1 {
		t.Errorf("got %d suggestions for a two-member LAG, want 1", len(out))
	}
}

// An FQDN sysName is usually written as just its host part.
func TestMatchesFirstLabelOfAnFQDN(t *testing.T) {
	out := SuggestFromDescriptions(fleet, []IfaceRef{
		{DeviceID: "d_edge", IfIndex: 2, Name: "Gi0/2", Alias: "xconnect core-sw-01"},
	})
	find(t, out, "d_edge", "d_core")
}

// Ambiguity must produce nothing rather than a confident link to the wrong box.
func TestAmbiguousMatchIsDropped(t *testing.T) {
	twins := []DeviceRef{
		{ID: "d_1", Name: "SW-01", SysName: "sw-01"},
		{ID: "d_2", Name: "SW-01", SysName: "sw-01-b"},
		{ID: "d_src", Name: "SRC", SysName: "src"},
	}
	out := SuggestFromDescriptions(twins, []IfaceRef{
		{DeviceID: "d_src", IfIndex: 1, Name: "Gi0/1", Alias: "to SW-01"},
	})
	if len(out) != 0 {
		t.Errorf("suggested %d links from an ambiguous name", len(out))
	}
}

// Descriptions vary wildly in punctuation; the same intent must tokenise alike.
func TestPunctuationIsIgnored(t *testing.T) {
	out := SuggestFromDescriptions(fleet, []IfaceRef{
		{DeviceID: "d_edge", IfIndex: 9, Name: "Gi0/9", Descr: "[TO:CORE-SW-01](Gi0/3)"},
	})
	find(t, out, "d_edge", "d_core")
}
