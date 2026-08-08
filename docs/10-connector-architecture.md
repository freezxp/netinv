# 10 — Connector Architecture (Plugin Framework)

**Status:** review · **Depends on:** 05, ADR-014 · **Constrains:** 13, 17

## 1. Purpose & principle

A **connector** is the only place vendor-specific knowledge lives: which OIDs to walk, how to normalize values, which quirks to work around. The core (scheduler, poller runtime, ingester, API) is vendor-blind. **Adding a platform = adding one package under `connectors/` + one import line in the registry. Zero core diffs (NFR-72).**

## 2. Interface contract

The kickoff brief named .NET-style interfaces (`IConnector`, `INetworkCollector`, `IHealthCollector`, `IVirtualMachineCollector`, `IHostCollector`, `IStorageCollector`). Go maps them as below; the VM/Host/Storage collectors are defined now (future virtualization/host roadmap, doc 29) but no v1 connector implements them — the framework treats every collector as optional capability.

```go
// connectors/sdk — the only package a connector may depend on from core.

type Connector interface {
    Info() Info                      // ID, vendor, version, capabilities
    Match(sys SysInfo) MatchScore    // sysObjectID/sysDescr auto-match (discovery, doc 11)
    Collectors() Capabilities        // which optional interfaces are implemented
}

type Info struct {
    ID, Vendor, DisplayName, Version string
    SysObjectIDPrefixes []string     // e.g. ".1.3.6.1.4.1.2636" (Juniper)
}

// Session abstracts transport. v1: SNMP implementation provided by the poller
// runtime (gosnmp). Future connectors may receive HTTP/API sessions (UniFi).
type Session interface {
    Get(ctx context.Context, oids []string) ([]Var, error)
    Walk(ctx context.Context, rootOID string) ([]Var, error)  // GETBULK w/ fallback
    Target() TargetMeta
}

// ---- Collector capabilities (all optional; declared via Collectors()) ----

type InventoryCollector interface {   // ~ brief's IConnector inventory duty
    CollectInventory(ctx context.Context, s Session) (*InventorySnapshot, error)
}
type InterfaceCollector interface {   // ~ INetworkCollector (traffic family)
    CollectInterfaces(ctx context.Context, s Session) ([]Sample, error)
}
type HealthCollector interface {      // ~ IHealthCollector (CPU/mem/temp/fan/PSU/optics)
    CollectHealth(ctx context.Context, s Session) ([]Sample, error)
}
type TopologyCollector interface {    // LLDP/CDP adjacency
    CollectTopology(ctx context.Context, s Session) ([]Adjacency, error)
}

// Future roadmap capabilities (defined, unimplemented in v1):
type VirtualMachineCollector interface { CollectVMs(ctx, Session) ([]VMRecord, error) }
type HostCollector interface           { CollectHost(ctx, Session) (*HostRecord, error) }
type StorageCollector interface        { CollectStorage(ctx, Session) ([]StorageRecord, error) }
```

**Normalized outputs** (`Sample`, `InventorySnapshot`, `Adjacency`) are core-defined value types: metric name from the canonical schema (doc 05 §6), unit-normalized values (octets stay counters; temperatures in °C; power in dBm). A connector never talks to VictoriaMetrics, RabbitMQ, or Postgres — it turns `Session` reads into normalized values, nothing else. That is what keeps connectors trivially testable (doc 24: recorded-walk fixtures) and extractable to out-of-process plugins later.

## 3. Registration & loading

```go
// connectors/registry/registry.go — imported ONLY by cmd/poller (doc 13 rule 5)
package registry
import (
    _ "github.com/freezxp/netinv/connectors/generic"  // side-effect init()
    _ "github.com/freezxp/netinv/connectors/cisco"
    _ "github.com/freezxp/netinv/connectors/juniper"
    _ "github.com/freezxp/netinv/connectors/huawei"
    _ "github.com/freezxp/netinv/connectors/zte"
    _ "github.com/freezxp/netinv/connectors/ubiquiti"
    _ "github.com/freezxp/netinv/connectors/ruckus"
)
```

Each connector package calls `sdk.Register(New())` in `init()`. The poller resolves `device.connector_id → Connector` at job time; the API seeds the `platform.connectors` catalog table from the same registry at startup (doc 08). Version skew between poller and core is tolerated: connector ID + version travel in every batch, surfaced in the poller fleet UI.

**Out-of-process seam (future, ADR-014):** the `Connector` interface is deliberately context+value-based (no channels, no shared state) so a `grpcConnector` adapter can proxy it over hashicorp/go-plugin for third-party or non-Go connectors without changing the SDK contract.

## 4. Layered collection strategy

Every device gets standards-based collection for free; vendor connectors *extend*, never reimplement:

```
┌────────────────────────────────────────────────┐
│ vendor connector (cisco, juniper, …)           │  vendor MIBs: CPU, memory, temp,
│   embeds ↓ and overrides/extends               │  fans, PSU, optics, stack
├────────────────────────────────────────────────┤
│ generic connector (connectors/generic)         │  IF-MIB, SNMPv2-MIB (system),
│   IF-MIB walk, sysXxx, ENTITY-MIB best-effort, │  ENTITY-MIB, LLDP-MIB
│   LLDP-MIB                                     │
└────────────────────────────────────────────────┘
```

Go composition: `type cisco struct { generic.Base }` — override `CollectHealth`, inherit the rest. Unknown devices run pure `generic` (traffic + system + best-effort health), so *any* RFC-compliant agent is monitorable day one (NFR-62).

## 5. v1 vendor matrix

| Connector | sysObjectID prefix | Health sources (beyond IF-MIB) | Notes |
|---|---|---|---|
| `generic` | * (fallback) | **UCD-SNMP-MIB** (CPU idle→busy, load average, memory with buffers/cache excluded) and **LM-SENSORS** temperatures — every net-snmp agent exposes these, so Linux-based appliances and servers get health for free | matches anything, lowest score. **Interface speed reads ifHighSpeed first, then ifSpeed.** ifHighSpeed is preferred because ifSpeed is a 32-bit gauge that saturates around 4.29 Gbit/s, but agents that leave ifHighSpeed at 0 are common — a Ruckus R710 reports 1000000000 in ifSpeed and 0 in ifHighSpeed on every port. Reading only ifHighSpeed published a speed of 0, and a zero denominator pins utilization at 0% however busy the link is |
| `cisco-ios` | .1.3.6.1.4.1.9 | CISCO-PROCESS-MIB (cpmCPUTotal5minRev), CISCO-MEMORY-POOL / CISCO-ENHANCED-MEMPOOL, CISCO-ENVMON + CISCO-ENTITY-SENSOR (temp/fan/PSU), CISCO-ENTITY-FRU-CONTROL, CISCO-STACK, optics via entSensor | covers IOS/IOS-XE; NX-OS quirks via sub-profile field in `attrs` |
| `juniper-junos` | .1.3.6.1.4.1.2636 | JUNIPER-MIB jnxOperatingTable (CPU/mem/temp per FRU), jnxFruTable (PSU/fan), JUNIPER-DOM-MIB (optics dBm) | |
| `huawei-vrp` | .1.3.6.1.4.1.2011 | HUAWEI-ENTITY-EXTENT-MIB (hwEntityCpuUsage/MemUsage/Temperature), HUAWEI-ENERGY, hwOpticalModuleInfo | |
| `ruckus` | .1.3.6.1.4.1.25053 (+ sysDescr) | RUCKUS-UNLEASHED-MIB: managed-AP count, connected clients, per-AP up/total; vendor/model/serial/firmware for inventory | **Validated against a real R710 (Unleashed 200.15.6.212)**: exposes *no* CPU/memory/temperature anywhere — not its own MIB, not UCD-SNMP, not HOST-RESOURCES — so this connector deliberately reports none rather than inventing them. Client-count OID semantics are inferred from the stats group and pending operator confirmation. **Mesh:** the MIB carries no mesh objects at all, but the per-AP ethernet sub-table (`…2.1.1.2.8.1`) reports link state per port, so an AP with every port down is necessarily mesh-joined. On the root AP the backhaul is `wlan62` — `wlan0–7`/`wlan32–39` are the service BSSID slots, `wlan62/63` sit outside them, and on a root the downlink is up while the uplink half stays admin-down. Member APs run no SNMP agent of their own (port 161 refuses), so the root is the only pollable node |
| `zte-zxr` | .1.3.6.1.4.1.3902 | ZTE-AN / zxr10 system MIBs (CPU/mem/temp); ENTITY-SENSOR fallback is load-bearing — ZTE MIB coverage varies by line; validate against real units Sprint 17 (risk R-07) | |
| `ubiquiti` | .1.3.6.1.4.1.41112 (+ .10002 airOS), **plus sysDescr match** | inherits the generic UCD-SNMP/LM-SENSORS health set | **Validated against real UDM-Pro hardware**: UniFi OS consoles run stock net-snmp and report sysObjectID `.1.3.6.1.4.1.8072.3.2.10`, so prefix matching alone misses them — hence the sysDescr fallback. They expose UCD-SNMP + LM-SENSORS but *not* the UniFi MIB, HOST-RESOURCES, or LLDP. UniFi Controller REST connector is roadmap (ADR/C3). **Site-to-site SD-WAN tunnels** surface in IF-MIB as WireGuard interfaces named `wgstsNNNN`, and the tunnel number is the *same on both endpoints* — that pairs them into weathermap links without any controller API. On gateways that populate it, `ifAlias` also names the peer outright (`Magic S2S VPN: FN Gateway(Default) - AL Gateway(Default)`), though it is empty on some units, so match on the `wgsts` number and treat the alias as a bonus. These interfaces report `ifSpeed 0`, so weathermap links over them need a capacity set by hand or they never colour |

Each connector ships: OID map file, recorded-walk test fixtures from real hardware (`testdata/*.snmpwalk`), capability declaration, and a `docs/` note listing verified models/OS versions.

## 6. Adding a new platform — the checklist (also in connector README template)

1. `mkdir connectors/<name>`; implement `Connector` embedding `generic.Base`.
2. Add OID map + normalization; declare capabilities honestly.
3. Record `snmpwalk` fixtures from a real device; write table-driven tests against them (≥90% coverage of the mapping code).
4. Register in `connectors/registry`; run `make connector-lint` (verifies: no imports outside `connectors/sdk` + stdlib, capability/test presence).
5. PR must show zero diffs outside `connectors/` (+registry line). CI enforces via path check.
