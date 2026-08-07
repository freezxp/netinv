# 15 — Entity Relationship Model (Logical)

**Status:** draft · **Depends on:** 16 · physical realization: 08

This is the storage-agnostic view: entities, identity, lifecycles, and relationship semantics. Doc 08 maps it to PostgreSQL; the domain layer (doc 16/17) maps it to Go types. When the three disagree, this document is the tie-breaker for *meaning*, doc 08 for *storage*.

## 1. Entity catalog

| Entity | Identity | Mutability | Lifecycle states |
|---|---|---|---|
| Tenant | ULID | config | active (dormant v1) |
| User | ULID | config | active → locked ⇄ active → deactivated |
| Role | name | config (builtin) / custom later | — |
| Site | ULID | config | active → disabled |
| Poller | ULID | self-registering | pending → active ⇄ disabled |
| Connector | string ID (code-defined) | code release | enabled/disabled |
| Credential | ULID | config, secret write-only | in-use (ref-counted) → deletable |
| PollingProfile | ULID | config | — |
| Device | ULID | mixed: operator fields vs network-reported fields (doc 11 §3) | pending → active ⇄ unreachable → disabled → retired |
| Interface | Device + ifIndex (logical identity survives reindex, FR-DEV-09) | network-reported + `monitor` flag | present ⇄ missing → removed |
| DeviceComponent | Device + kind + source index | network-reported | present ⇄ missing → removed |
| TopologyLink | endpoints tuple | network-reported | active ⇄ stale |
| AssetChange | sequence | **immutable** | append-only |
| SyncRun | ULID | system | running → ok/partial/failed |
| Map | ULID | operator | draft revisions → published revision |
| MapRevision | Map + rev | immutable once published | draft → published → archived |
| AlertRule | ULID | config (builtin pack: limited edits) | enabled/disabled |
| AlertInstance | rule + fingerprint | system + ack action | firing → acknowledged → resolved; flapping overlay |
| AlertEvent | sequence | **immutable** | append-only |
| Silence | ULID | operator | scheduled → active → expired/revoked |
| NotificationChannel | ULID | config, secret write-only | enabled/disabled |
| NotificationPolicy | ULID | config | ordered list |
| Delivery | sequence | system | queued → ok/retrying/failed |
| AuditEvent | sequence | **immutable** | append-only |
| Setting | key | config | — |

## 2. Relationship semantics

- **Site ⟶ Device (1:N, mandatory):** a device always belongs to exactly one site; the site determines which poller pool touches it. Moving a device between sites re-routes its jobs next scheduler tick.
- **Site ⟶ Poller (1:N):** pollers are site-bound (FR-PLT-04). Pollers within a site are interchangeable (competing consumers).
- **Credential ⟶ Device (1:N, RESTRICT):** shared secrets; deletion blocked while referenced (FR-CRED-02).
- **Connector ⟶ Device (1:N):** the driver binding; changed only by operator (or discovery match at onboarding).
- **Device ⟶ Interface/Component (1:N, owned):** children share the device's lifecycle; device retirement freezes (not deletes) children.
- **Device ⟷ Group (M:N):** organizational only — no behavior attaches to groups except alert scoping and filters.
- **Interface ⟷ TopologyLink:** a link joins two ports; the far end may be unmanaged (recorded as-reported strings until/unless the neighbor is onboarded, then resolved to a device reference by the sync differ).
- **Map ⟶ Interface (M:N via link endpoints):** maps *reference* inventory, never own it; a removed interface degrades the map link (grey) rather than breaking the map (FR-SYNC-03 → doc 11 §4).
- **AlertRule ⟶ AlertInstance (1:N):** instances are facts about the world at a time, not config — they survive rule edits (instance copies severity/labels at fire time).
- **AlertInstance ⟶ Delivery (1:N):** delivery is per channel per lifecycle event.
- **Everything ⟶ AuditEvent:** via events, not FKs — audit references `(resource_kind, resource_id)` loosely so purged resources don't break the audit trail (append-only invariant beats referential purity here; deliberate).

## 3. Invariants (enforced in domain layer, backstopped by DB constraints)

1. A device has exactly one credential, connector, profile, and site at all times.
2. At most one non-resolved AlertInstance per (rule, fingerprint).
3. Append-only entities are never updated or deleted by application code (DB role lacks the grants — doc 08).
4. Secret material exists only in encrypted columns and only transits create/update requests (write-only).
5. Operator-owned vs network-owned fields are disjoint sets (doc 11 §3) — no field is writable by both sync and operators.
6. A published MapRevision is immutable; edits fork a new draft revision.
7. `tenant_id` is present on every business entity and consistent across every relationship (future-proofing, ADR-005).

## 4. Time-series data (outside this model)

Samples are **not entities** — they are facts keyed by (metric, label set, timestamp) living in VictoriaMetrics with retention tiers (doc 04 §4). The bridge to the entity world is the label pair `device_id`/`if_index` (doc 05 §6): entities never store metric values; panels join at query time.
