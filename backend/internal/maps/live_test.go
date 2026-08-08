package maps

import (
	"encoding/json"
	"strings"
	"testing"
)

// A map with no links rendered as {"links": null}, and the viewer crashed
// iterating it. Both payloads must always carry arrays.
func TestEmptyPayloadsMarshalAsArraysNotNull(t *testing.T) {
	raw, err := json.Marshal(newLiveData())
	if err != nil {
		t.Fatalf("marshal live: %v", err)
	}
	for _, field := range []string{`"nodes":[]`, `"links":[]`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("live payload missing %s, got %s", field, raw)
		}
	}

	def := &Definition{Schema: "netinv.map/1"}
	def.normalize()
	raw, err = json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	for _, field := range []string{`"nodes":[]`, `"links":[]`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("definition missing %s, got %s", field, raw)
		}
	}
}

// A definition decoded from stored JSON with explicit nulls must come back
// iterable.
func TestDefinitionNormalizeRescuesStoredNulls(t *testing.T) {
	var def Definition
	if err := json.Unmarshal([]byte(`{"schema":"netinv.map/1","nodes":null,"links":null}`), &def); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	def.normalize()
	if def.Nodes == nil || def.Links == nil {
		t.Fatalf("normalize left nil slices: %+v", def)
	}
}

// Handle sides are cosmetic and must survive a round trip, while staying
// absent for links drawn before they were recorded.
func TestLinkHandleRoundTrip(t *testing.T) {
	raw, err := json.Marshal(Link{ID: "l1", From: "a", To: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "from_handle") {
		t.Errorf("unset handles should be omitted, got %s", raw)
	}

	raw, _ = json.Marshal(Link{ID: "l1", From: "a", To: "b", FromHandle: "r", ToHandle: "l"})
	var back Link
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.FromHandle != "r" || back.ToHandle != "l" {
		t.Errorf("handles did not round trip: %+v", back)
	}
}
