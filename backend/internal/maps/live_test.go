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

// ---- link capacity resolution (FR-MAP-08) ----

func ep(dev string, idx int) *Endpoint { return &Endpoint{DeviceID: dev, IfIndex: idx} }

func TestLinkCapacityPrefersAnExplicitSetting(t *testing.T) {
	l := Link{BandwidthBPS: 50e6, AEndpoint: ep("a", 1), BEndpoint: ep("b", 1)}
	wan := map[string]float64{"a": 100e6, "b": 200e6}
	if got := linkCapacity(l, 1e9, wan); got != 50e6 {
		t.Errorf("explicit capacity ignored: got %v", got)
	}
}

// Physical links must keep behaving exactly as before: ifSpeed wins over the
// uplink rate, which describes the circuit, not a LAN port.
func TestLinkCapacityUsesInterfaceSpeedWhenReported(t *testing.T) {
	l := Link{AEndpoint: ep("a", 1), BEndpoint: ep("b", 1)}
	wan := map[string]float64{"a": 100e6, "b": 200e6}
	if got := linkCapacity(l, 1e9, wan); got != 1e9 {
		t.Errorf("ifSpeed should win for a physical link: got %v", got)
	}
}

// The tunnel case: no ifSpeed, so the bottleneck is the slower circuit.
func TestLinkCapacityTakesTheSlowerEnd(t *testing.T) {
	l := Link{AEndpoint: ep("a", 43), BEndpoint: ep("b", 37)}
	wan := map[string]float64{"a": 500e6, "b": 100e6}
	if got := linkCapacity(l, 0, wan); got != 100e6 {
		t.Errorf("got %v, want the slower end (100e6)", got)
	}
	// Order must not matter.
	if got := linkCapacity(Link{AEndpoint: ep("b", 37), BEndpoint: ep("a", 43)}, 0, wan); got != 100e6 {
		t.Errorf("reversed link gave %v, want 100e6", got)
	}
}

// A half-known bottleneck is not a bottleneck. Falling back to the known end
// would overstate capacity and under-report utilisation.
func TestLinkCapacityUnknownWhenEitherEndIsUnset(t *testing.T) {
	l := Link{AEndpoint: ep("a", 43), BEndpoint: ep("b", 37)}
	for name, wan := range map[string]map[string]float64{
		"b missing": {"a": 500e6},
		"a missing": {"b": 500e6},
		"neither":   {},
		"b zero":    {"a": 500e6, "b": 0},
	} {
		if got := linkCapacity(l, 0, wan); got != 0 {
			t.Errorf("%s: got %v, want 0 (uncoloured)", name, got)
		}
	}
}

// An unbound link has no endpoints to reason about and must stay uncoloured.
func TestLinkCapacityUnboundLink(t *testing.T) {
	if got := linkCapacity(Link{}, 0, map[string]float64{"a": 1e9}); got != 0 {
		t.Errorf("unbound link got capacity %v, want 0", got)
	}
}

// One bound end is enough when it is the only end described.
func TestLinkCapacitySingleBoundEndpoint(t *testing.T) {
	l := Link{AEndpoint: ep("a", 43)}
	if got := linkCapacity(l, 0, map[string]float64{"a": 300e6}); got != 300e6 {
		t.Errorf("got %v, want 300e6", got)
	}
}

// ---- definition validation (FR-MAP-02) ----

func TestValidateAcceptsPlainNodes(t *testing.T) {
	d := &Definition{Nodes: []Node{
		{ID: "n1", Kind: "device", DeviceID: "d1"},
		{ID: "n2", Kind: "cloud", Label: "Internet"},
		{ID: "n3", Kind: "site", Label: "Branch"},
		{ID: "n4", Kind: "label", Label: "core"},
	}, Links: []Link{{ID: "l1", From: "n1", To: "n2"}}}
	if err := d.Validate(); err != nil {
		t.Fatalf("a valid map with plain nodes was rejected: %v", err)
	}
}

func TestValidateRejectsBrokenDocuments(t *testing.T) {
	for name, d := range map[string]*Definition{
		"unknown kind":          {Nodes: []Node{{ID: "n1", Kind: "rectangle"}}},
		"device with no device": {Nodes: []Node{{ID: "n1", Kind: "device"}}},
		"node with no id":       {Nodes: []Node{{Kind: "cloud"}}},
		"duplicate ids": {Nodes: []Node{
			{ID: "n1", Kind: "cloud"}, {ID: "n1", Kind: "cloud"},
		}},
		// A link to a node that isn't there renders as nothing at all.
		"dangling link": {
			Nodes: []Node{{ID: "n1", Kind: "cloud"}},
			Links: []Link{{ID: "l1", From: "n1", To: "ghost"}},
		},
		"link with no id": {
			Nodes: []Node{{ID: "n1", Kind: "cloud"}, {ID: "n2", Kind: "cloud"}},
			Links: []Link{{From: "n1", To: "n2"}},
		},
	} {
		if err := d.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Plain nodes carry no device, so the live assembler must leave them alone
// rather than report them as an unreachable device.
func TestPlainNodesTakeNoDeviceState(t *testing.T) {
	d := &Definition{Nodes: []Node{{ID: "n1", Kind: "cloud", Label: "Internet"}}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.Nodes[0].DeviceID != "" {
		t.Error("a cloud node acquired a device binding")
	}
}
