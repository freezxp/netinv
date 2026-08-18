package maps

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// Auto-generated weathermaps (FR-MAP-06).
//
// Drawing a map by hand is the flagship feature and stays that way — an
// operator's map says what they think matters. But the first map is the
// expensive one: on a fleet of any size it means placing every device and
// binding every link before seeing anything at all, and the topology the
// device already reports over LLDP is exactly that information. Generating a
// starting point turns an hour of dragging into an edit.
//
// It produces a draft, never a published map. What comes out is a truthful
// picture of what LLDP saw, not a considered diagram, and publishing it is the
// operator's decision after they have moved things where they belong.

// topoEdge is one adjacency between two managed devices, with the direction
// collapsed out. LLDP reports A→B and B→A separately and a map wants one line,
// but each direction carries the ifIndex of its *own* end — so both are kept
// and merged, or the link binds one side and silently graphs nothing on the
// other.
type topoEdge struct {
	aDeviceID, bDeviceID string
	aIfIndex, bIfIndex   int
}

// pairKey orders two device ids so A→B and B→A hash to the same edge.
func pairKey(x, y string) (string, string, bool) {
	if x <= y {
		return x, y, false
	}
	return y, x, true
}

// BuildTopologyDefinition turns LLDP adjacencies into a map document.
//
// Pure and deterministic: the same suggestions always produce the same
// document, node ids included. That matters because regenerating is a normal
// thing to do after the estate changes, and a diff that reshuffles every id
// tells an operator nothing about what actually moved.
func BuildTopologyDefinition(name string, sugs []Suggestion) Definition {
	// Only adjacencies where both ends are devices NetInv manages. An LLDP
	// neighbour that is not in inventory has no interfaces to graph, so a node
	// for it would be a label that never shows traffic — worse than absent,
	// because it looks like a link that is always idle.
	edges := map[[2]string]*topoEdge{}
	names := map[string]string{}
	for _, s := range sugs {
		if s.ADeviceID == "" || s.BDeviceID == "" {
			continue
		}
		if s.ADeviceID == s.BDeviceID {
			continue // a device reporting itself; nothing to draw
		}
		names[s.ADeviceID] = s.ADevice
		if s.BDevice != "" {
			names[s.BDeviceID] = s.BDevice
		}
		lo, hi, flipped := pairKey(s.ADeviceID, s.BDeviceID)
		k := [2]string{lo, hi}
		e := edges[k]
		if e == nil {
			e = &topoEdge{aDeviceID: lo, bDeviceID: hi}
			edges[k] = e
		}
		// Attach this row's ifIndex to whichever end it describes.
		if !flipped {
			if s.AIfIndex > 0 {
				e.aIfIndex = s.AIfIndex
			}
		} else if s.AIfIndex > 0 {
			e.bIfIndex = s.AIfIndex
		}
	}

	// Sorted so layout and ids are stable across runs.
	deviceIDs := make([]string, 0, len(names))
	for id := range names {
		deviceIDs = append(deviceIDs, id)
	}
	sort.Strings(deviceIDs)

	def := Definition{Schema: "netinv.map/1", Name: name}
	def.normalize()
	nodeOf := map[string]string{}
	for i, did := range deviceIDs {
		x, y := layoutPosition(i, len(deviceIDs))
		nid := fmt.Sprintf("n%d", i+1)
		nodeOf[did] = nid
		def.Nodes = append(def.Nodes, Node{
			ID: nid, Kind: "device", DeviceID: did,
			Label: names[did], X: x, Y: y,
		})
	}

	keys := make([][2]string, 0, len(edges))
	for k := range edges {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for i, k := range keys {
		e := edges[k]
		l := Link{
			ID:   fmt.Sprintf("l%d", i+1),
			From: nodeOf[e.aDeviceID],
			To:   nodeOf[e.bDeviceID],
		}
		// An endpoint is bound only when its ifIndex is known. Binding a guess
		// would draw a link that graphs the wrong port, which is harder to
		// notice than one that graphs nothing.
		if e.aIfIndex > 0 {
			l.AEndpoint = &Endpoint{DeviceID: e.aDeviceID, IfIndex: e.aIfIndex}
		}
		if e.bIfIndex > 0 {
			l.BEndpoint = &Endpoint{DeviceID: e.bDeviceID, IfIndex: e.bIfIndex}
		}
		def.Links = append(def.Links, l)
	}
	return def
}

// layoutPosition places node i of n. A ring reads well while every node can
// see every other one; past that the crossings make it worse than a grid, and
// neither is a substitute for an operator moving things where they belong —
// which is why this produces a draft.
func layoutPosition(i, n int) (float64, float64) {
	const (
		cx, cy   = 500.0, 380.0
		radius   = 300.0
		gridStep = 190.0
		gridX0   = 120.0
		gridY0   = 120.0
	)
	if n <= 1 {
		return cx, cy
	}
	if n <= 16 {
		a := 2 * math.Pi * float64(i) / float64(n)
		// Start at the top and go clockwise, so a two-node map reads
		// vertically rather than as a line through the middle.
		return round(cx + radius*math.Sin(a)), round(cy - radius*math.Cos(a))
	}
	cols := int(math.Ceil(math.Sqrt(float64(n))))
	return gridX0 + float64(i%cols)*gridStep, gridY0 + float64(i/cols)*gridStep
}

func round(v float64) float64 { return math.Round(v) }

// GenerateFromTopology creates a draft map from the LLDP adjacencies between
// managed devices. It refuses rather than creating an empty map: a map with no
// nodes is indistinguishable from a broken generator, and the useful answer is
// why there was nothing to draw.
func (s *Store) GenerateFromTopology(ctx context.Context, name, createdBy string) (*MapMeta, int, int, error) {
	sugs, err := s.Suggestions(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	def := BuildTopologyDefinition(name, sugs)
	if len(def.Nodes) == 0 {
		return nil, 0, 0, errx.New(errx.KindInvalid,
			"no topology to draw: no LLDP adjacency between two managed devices. "+
				"Both ends have to be devices NetInv polls, and the neighbour has to "+
				"be matched to one — check the Neighbors tab on a device.")
	}
	meta, err := s.Create(ctx, name, createdBy)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := s.SaveDraft(ctx, meta.ID, &def, createdBy); err != nil {
		return nil, 0, 0, err
	}
	return meta, len(def.Nodes), len(def.Links), nil
}
