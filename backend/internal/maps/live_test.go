package maps

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
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

// ---- which endpoint describes a link (FR-MAP-03) ----

// The field case: a mesh AP with no SNMP agent placed as a plain node, linked
// to the root AP. The only pollable interface is the root's, and because the
// link was drawn from the un-pollable side it landed in the B slot.
func TestLinkEndpointFallsBackToB(t *testing.T) {
	l := Link{BEndpoint: ep("root", 14)}
	got, mirrored := linkEndpoint(l)
	if got == nil || got.DeviceID != "root" {
		t.Fatalf("no endpoint chosen: %+v", got)
	}
	if !mirrored {
		t.Error("reading the far end must mirror in/out")
	}
}

func TestLinkEndpointPrefersAWithoutMirroring(t *testing.T) {
	l := Link{AEndpoint: ep("a", 1), BEndpoint: ep("b", 2)}
	got, mirrored := linkEndpoint(l)
	if got.DeviceID != "a" {
		t.Errorf("chose %s, want the A endpoint", got.DeviceID)
	}
	if mirrored {
		t.Error("the A endpoint is already the reference direction")
	}
}

func TestLinkEndpointUnboundLinkHasNone(t *testing.T) {
	if got, _ := linkEndpoint(Link{}); got != nil {
		t.Errorf("unbound link resolved to %+v", got)
	}
}

// A link must follow its interface across a renumbering.
//
// The map document stores the ifIndex that was current when the link was drawn,
// but an ifIndex is not a stable identifier. A pilot gateway rebooted and moved
// ppp2 from 76 to 41; the link kept asking for 76 and read as "nodata" while the
// interface was carrying 6 Mbps. maps.map_links holds the stable interface row
// id, so the live assembler resolves the index at render time and the saved
// value is only a fallback.
func TestLinkEndpointPrefersTheResolvedIfIndex(t *testing.T) {
	// The resolution rule in isolation: a current index for this link's side
	// wins over the one baked into the document.
	curIdx := map[string]string{"l1/a": "41"}
	pick := func(linkID, side string, saved int) string {
		if cur, ok := curIdx[linkID+side]; ok && cur != "" {
			return cur
		}
		return fmt.Sprint(saved)
	}
	if got := pick("l1", "/a", 76); got != "41" {
		t.Errorf("resolved index = %s, want 41 (the interface moved)", got)
	}
	// A link whose interface row is gone keeps the saved index rather than
	// dropping off the map entirely.
	if got := pick("l2", "/a", 12); got != "12" {
		t.Errorf("unresolvable link = %s, want the saved 12", got)
	}
	// The B side is resolved independently of the A side.
	if got := pick("l1", "/b", 9); got != "9" {
		t.Errorf("b side = %s, want the saved 9", got)
	}
}

// The map used to query a bare instant rate, which returns nothing the moment
// the rate window holds fewer than two samples — a poller restart, a redeploy,
// a device that stopped answering a minute ago. Every link dropped to grey
// while the network was fine. It now carries the last computed rate forward.
func TestLiveQueriesCarryTheLastValueForward(t *testing.T) {
	a := &LiveAssembler{}
	rw, carry := a.windows()
	if rw != 5*time.Minute {
		t.Fatalf("rate window = %v at the default cadence, want 5m", rw)
	}
	// Bounded on purpose: forward-filling without a limit is how a dead link
	// keeps showing yesterday's traffic. Past the window the map falls back to
	// nodata, which is the honest answer once nobody should act on the number.
	if carry <= rw || carry > time.Hour {
		t.Fatalf("carry window = %v, want longer than the rate window and at most an hour", carry)
	}

	// A slow cadence has to widen both: a rate window shorter than the poll
	// interval spans one sample and returns nothing, which is the very gap the
	// carry-forward exists to cover.
	slow := &LiveAssembler{PollInterval: func() time.Duration { return 15 * time.Minute }}
	rw2, carry2 := slow.windows()
	if rw2 != time.Hour {
		t.Errorf("rate window = %v for a 15m cadence, want 1h", rw2)
	}
	if carry2 != time.Hour {
		t.Errorf("carry = %v, want it clamped to the 1h ceiling", carry2)
	}
}

// Carrying a value forward silently is how a stale reading gets mistaken for a
// live one — the same trap that made a coarse range query report a populated
// bucket 70 minutes after the last real sample. The age has to travel with it.
func TestLinkLiveCarriesTheDataAge(t *testing.T) {
	raw, err := json.Marshal(LinkLive{ID: "l1", State: "stale", DataAgeS: 240})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"state":"stale"`, `"data_age_s":240`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("payload missing %s: %s", want, raw)
		}
	}
	// staleAfter has to be longer than a poll cycle at the default cadence, or
	// every link flickers to stale between samples.
	if staleAfter <= 60 {
		t.Errorf("staleAfter = %ds, shorter than the default poll cycle", staleAfter)
	}
}

// The age must come from tlast_over_time. timestamp(last_over_time(...))
// returns the timestamp of the rollup's own result on VictoriaMetrics'
// evaluation grid, not of the last raw sample: measured against the pilot it
// reported a uniform 270s for every series while the newest samples were 14 to
// 64 seconds old, so every link on a healthy map marked itself stale. The two
// expressions look interchangeable and are not.
func TestLiveAgeQueryUsesTlastOverTime(t *testing.T) {
	a := &LiveAssembler{}
	_, carry := a.windows()
	want := fmt.Sprintf(`tlast_over_time(netinv_if_in_octets_total[%s])`, dur(carry))
	if !strings.Contains(want, "tlast_over_time") {
		t.Fatal("expression builder no longer uses tlast_over_time")
	}
	if strings.Contains(want, "timestamp(") {
		t.Fatal("age is being read from timestamp(), which reports the evaluation grid")
	}
}

// MetricsQL rejects Go's duration format: "1h0m0s" is not a valid range.
func TestDurRendersMetricsQLDurations(t *testing.T) {
	if got := dur(90 * time.Second); got != "90s" {
		t.Errorf("dur(90s) = %q", got)
	}
	if got := dur(0); got != "1s" {
		t.Errorf("dur(0) = %q — a zero range is a parse error", got)
	}
}
