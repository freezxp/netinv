// Package sdk is the connector plugin contract (doc 10). It is the ONLY
// package a connector may import from NetInv, and it imports nothing outside
// the standard library — that is what keeps connectors testable in isolation
// and extractable to out-of-process plugins later (ADR-014).
package sdk

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---- identity & capability ----

type Info struct {
	ID                  string // e.g. "cisco-ios"
	Vendor              string
	DisplayName         string
	Version             string
	SysObjectIDPrefixes []string // e.g. ".1.3.6.1.4.1.9"
}

type Capability string

const (
	CapInventory  Capability = "inventory"
	CapInterfaces Capability = "interfaces"
	CapHealth     Capability = "health"
	CapTopology   Capability = "topology"
)

type SysInfo struct {
	SysObjectID string
	SysDescr    string
}

// MatchScore: higher wins; 0 = no match. Generic returns 1 (universal floor).
type MatchScore int

type Connector interface {
	Info() Info
	Match(sys SysInfo) MatchScore
	Capabilities() []Capability
}

// ---- transport ----

type Var struct {
	OID   string
	Value any // int64, uint64, string, []byte per SNMP type mapping
}

type TargetMeta struct {
	Address string
	Port    int
}

// Session abstracts the transport the poller runtime provides (SNMP in v1).
type Session interface {
	Get(ctx context.Context, oids []string) ([]Var, error)
	// Walk iterates a subtree (GETBULK with GETNEXT fallback).
	Walk(ctx context.Context, rootOID string) ([]Var, error)
	Target() TargetMeta
}

// ---- normalized outputs ----

// Sample is one normalized metric observation (metric names per doc 05 §6).
type Sample struct {
	Name   string
	Labels map[string]string // bounded label values only — no free text
	Value  float64
	At     time.Time
}

type InterfaceRecord struct {
	IfIndex     int
	Name        string
	Alias       string
	Descr       string
	IfType      int
	MTU         int
	SpeedBPS    int64
	PhysAddress string
	AdminStatus int
	OperStatus  int
}

type InventorySnapshot struct {
	SysName     string
	SysDescr    string
	SysObjectID string
	SysLocation string
	SysContact  string
	UptimeS     int64
	// Vendor identity, when the connector can read it from vendor MIBs. The
	// generic layer leaves these empty; sync only writes non-empty values.
	Vendor    string
	Model     string
	Serial    string
	OSVersion string
	Interfaces []InterfaceRecord
}

type Adjacency struct {
	LocalIfIndex  int
	RemoteSysName string
	RemotePortID  string
	RemoteChassis string
	Protocol      string // lldp | cdp
}

// ---- optional collector capabilities ----

type InventoryCollector interface {
	CollectInventory(ctx context.Context, s Session) (*InventorySnapshot, error)
}
type InterfaceCollector interface {
	CollectInterfaces(ctx context.Context, s Session) ([]Sample, error)
}
type HealthCollector interface {
	CollectHealth(ctx context.Context, s Session) ([]Sample, error)
}
type TopologyCollector interface {
	CollectTopology(ctx context.Context, s Session) ([]Adjacency, error)
}

// ---- registry ----

var (
	regMu    sync.RWMutex
	registry = map[string]Connector{}
)

// Register is called from connector init() functions (ADR-014).
func Register(c Connector) {
	regMu.Lock()
	defer regMu.Unlock()
	id := c.Info().ID
	if _, dup := registry[id]; dup {
		panic(fmt.Sprintf("sdk: duplicate connector id %q", id))
	}
	registry[id] = c
}

func ByID(id string) (Connector, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	c, ok := registry[id]
	return c, ok
}

func All() []Connector {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Connector, 0, len(registry))
	for _, c := range registry {
		out = append(out, c)
	}
	return out
}

// BestMatch scores every registered connector against sys info.
func BestMatch(sys SysInfo) (Connector, MatchScore) {
	regMu.RLock()
	defer regMu.RUnlock()
	var best Connector
	var bestScore MatchScore
	for _, c := range registry {
		if s := c.Match(sys); s > bestScore {
			best, bestScore = c, s
		}
	}
	return best, bestScore
}

// PrefixScore is the standard sysObjectID matcher: longest matching prefix.
func PrefixScore(sys SysInfo, prefixes []string) MatchScore {
	best := 0
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(sys.SysObjectID, p) && len(p) > best {
			best = len(p)
		}
	}
	return MatchScore(best)
}
