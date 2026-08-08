# 03 — Functional Requirement Specification (FRS)

**Status:** draft · **Depends on:** 02

Requirement IDs: `FR-<MODULE>-<nn>`. Every requirement is testable; acceptance criteria are implied by the MUST/SHOULD phrasing and verified per doc 24. Modules: AUTH, RBAC, DEV (device/inventory), CRED, COLL (collection), MET (metrics), SYNC, ALR (alerting), NOT (notification), MAP (weathermap), DASH, EXP (export), AUD (audit), SET (settings), PLT (platform mgmt), API.

## AUTH — Authentication

- **FR-AUTH-01** Users MUST authenticate with username + password; passwords hashed with Argon2id (params in doc 20 §4).
- **FR-AUTH-02** Successful login MUST issue a JWT access token (TTL 15 min) and an opaque rotating refresh token (TTL 30 days, revocable, single-use rotation with reuse detection).
- **FR-AUTH-03** The system MUST support personal API tokens (scoped, expiring, revocable) for automation; tokens are shown once at creation.
- **FR-AUTH-04** Five consecutive failed logins for an account MUST lock it for 15 minutes and emit an audit event; lockout state is stored in Redis.
- **FR-AUTH-05** All authentication events (success, failure, lockout, logout, token refresh, token revocation) MUST be audit-logged with source IP and user agent.
- **FR-AUTH-06** Token verification MUST be behind a `TokenVerifier` interface so a future OIDC issuer (Keycloak) replaces local issuance by configuration (ADR-010).
- **FR-AUTH-07** Admins MUST be able to force password reset and deactivate users; deactivation invalidates all refresh/API tokens within 60 s.

## RBAC — Authorization

- **FR-RBAC-01** Four built-in roles: **Admin**, **Operator**, **Read-Only**, **Auditor**. Full permission matrix in doc 20 §5; summary: Admin = everything; Operator = ack/silence alerts, edit maps, run discovery, no user/settings/credential admin; Read-Only = view all except credentials and audit; Auditor = view-only including audit logs, cannot ack or change anything.
- **FR-RBAC-02** Every API endpoint MUST declare a required permission; requests lacking it return `403` with a machine-readable error (doc 23 §API errors).
- **FR-RBAC-03** A user MAY hold multiple roles; effective permissions are the union.
- **FR-RBAC-04** Role assignments are auditable (who granted what to whom, when).
- **FR-RBAC-05** The permission model MUST be resource-action based (`devices:write`, `alerts:ack`, …) so custom roles can be added post-v1 without endpoint changes.

## DEV — Device & inventory

- **FR-DEV-01** A device record MUST carry: display name, management IP (v4/v6), site, vendor/connector binding, credential reference, polling profile, enabled flag, tags, and free-text notes.
- **FR-DEV-02** Devices MUST be creatable singly (form/API) and in bulk via CSV import with per-row validation report.
- **FR-DEV-03** Inventory search MUST match name, IP, serial, model, sysDescr, and tag values with <1 s response at 100k devices (indexed per doc 08).
- **FR-DEV-04** Filters MUST compose (vendor ∧ site ∧ status ∧ firmware-version ∧ group) and be shareable as URL state.
- **FR-DEV-05** Devices MUST be groupable into named device groups (many-to-many) and by hierarchical sites.
- **FR-DEV-06** The device detail view MUST show: identity/inventory metadata, live status, interface table (with per-interface sparklines), health metrics, active alerts, topology neighbors, and asset history.
- **FR-DEV-07** Asset history MUST record every detected change (field, old, new, detected-at, source sync run) retained ≥ 12 months.
- **FR-DEV-08** Deleting a device MUST be soft (state `retired`) preserving history and metrics; hard purge is an Admin action with confirmation, cascading per doc 08.
- **FR-DEV-09** Interfaces MUST be tracked as first-class records keyed by device + ifIndex with rename survival (match on ifName/ifAlias when ifIndex shifts after reboot — doc 11 §identity).

## CRED — Credential management

- **FR-CRED-01** Credential sets (SNMP v2c community; SNMPv3 user/auth/priv) MUST be stored envelope-encrypted (ADR-011) and never returned in any API response after creation — write-only fields.
- **FR-CRED-02** Credentials are named, reusable across devices, and reference-counted; deletion is blocked while referenced.
- **FR-CRED-03** A "test credential" action MUST verify SNMP reachability against a target device and report the failure class (timeout / auth failure / priv failure) without exposing secret material.
- **FR-CRED-04** All credential create/update/delete/test events MUST be audit-logged (metadata only, never values).

## COLL — Collection

- **FR-COLL-01** Pollers MUST execute SNMP v2c and v3 (SHA-1/SHA-256 auth; AES-128/AES-256 priv; MD5/DES supported read-only for legacy but flagged deprecated in UI).
- **FR-COLL-02** Collection uses GETBULK where the device supports it, falling back to GETNEXT; max-repetitions tunable per polling profile.
- **FR-COLL-03** Counter metrics MUST use 64-bit HC counters when available (ifHCInOctets…); 32-bit fallback MUST handle wrap correctly.
- **FR-COLL-04** Polling profiles define cadence per metric family — defaults: traffic 60 s, health 300 s, ICMP 30 s, inventory 6 h (FR-SYNC) — and per-family enable/disable.
- **FR-COLL-05** ICMP probing MUST record RTT min/avg/max, jitter (RFC 3550-style variance), and loss % from a configurable probe count (default 5 probes/cycle).
- **FR-COLL-06** Every poll attempt MUST record outcome (ok / timeout / auth-error / partial) as a metric (`netinv_poll_success`) enabling SNMP-responsiveness dashboards and alerting.
- **FR-COLL-07** A device MUST be polled by exactly one poller at a time (site affinity; queue semantics per doc 05 §messaging prevent double-consumption).
- **FR-COLL-08** Pollers MUST batch metric submissions (default: flush at 500 samples or 5 s) and buffer locally up to a bounded size (default 15 min) during core-link outage, draining on reconnect; overflow drops oldest with a counter.
- **FR-COLL-09** Derived metrics MUST be computed at ingest: interface utilization % (rate ÷ ifHighSpeed), error rate %, and status-transition events (doc 05 §ingester).
- **FR-COLL-10** Per-device concurrent SNMP requests MUST be limited (default 1 walk at a time) to avoid overloading device control planes.

## SYNC — Discovery & synchronization (detail in doc 11)

- **FR-SYNC-01** Inventory sync MUST run per device on schedule (default 6 h) and on demand, collecting system, entity, interface, and LLDP/CDP tables.
- **FR-SYNC-02** Change detection MUST diff the collected snapshot against stored state field-by-field, writing asset-history entries and emitting `inventory.changed` events.
- **FR-SYNC-03** Deleted-asset detection: interfaces/components absent for N consecutive syncs (default 3) are marked `missing`, then `removed`; devices unreachable are marked `unreachable`, never auto-deleted.
- **FR-SYNC-04** Subnet discovery (P1) MUST sweep operator-provided CIDRs (ICMP + SNMP probe with candidate credential list), producing an approval queue; nothing is auto-managed.
- **FR-SYNC-05** Sync runs MUST be recorded (start, end, outcome, changes count, error detail) and visible in UI; failures retry per doc 23 policy.
- **FR-SYNC-06** Concurrent sync of the same device MUST be prevented via distributed lock (Redis, doc 05).

## ALR — Alerting

- **FR-ALR-01** Alert rules MUST support: metric threshold (MetricsQL expression, operator, value, sustained-for duration), state change (device down, link down, poll failing), and inventory events (new device, firmware changed).
- **FR-ALR-02** Severities: `critical`, `warning`, `info`. A rule targets a scope: all devices, a site, a group, a device, or an interface selector.
- **FR-ALR-09** Rules are operator-managed: create, edit, delete and enable/disable, all requiring `alerts:admin`. An expression is validated against the metrics backend **before** it is stored and the backend's own parse error is returned verbatim — an unparseable expression is otherwise accepted happily and the only symptom is an alert that never fires. The shipped pack (FR-ALR-07) is marked `is_builtin`: tunable and disable-able, but **not** deletable, since nothing short of a re-migration would restore one and their ids are referenced by docs and runbooks. Deleting a rule takes its alert history with it (`alert_instances` cannot be orphaned), so the confirmation states how many live alerts disappear with it and offers disabling as the alternative. Every mutation — including enable/disable, which silences monitoring — is audited with before/after values.
- **FR-ALR-03** Alert lifecycle: `firing` → `acknowledged` (by user, with comment) → `resolved` (auto when condition clears) ; every transition timestamped and audit-visible. Re-fire after resolve creates a new instance linked to the same rule.
- **FR-ALR-04** Evaluation cadence default 30 s; a breached rule MUST produce an alert within one evaluation cycle of the condition's `for` duration elapsing.
- **FR-ALR-05** Flap suppression: an alert resolving and re-firing >3 times in 15 min collapses into a single `flapping` alert until quiet for 15 min.
- **FR-ALR-06** Silences: user-created mute for a scope + duration with mandatory reason; silenced alerts still record state but notify nothing.
- **FR-ALR-07** Ships with a default rule pack: device down (ICMP fail 3 cycles) critical; interface down (oper≠admin) warning; utilization >80%/15m warning, >90% critical; CPU >85%/10m; memory >90%; temperature above vendor threshold; PSU/fan failed; device rebooted (sysUpTime reset) info; poll auth-failure warning.
- **FR-ALR-08** Interface alert suppression — two causes, both evaluated per device and only within a single rule's own results. Suppressed alerts are never created, so nothing needs acknowledging. Alerts carry `if_name` for readable summaries, added after fingerprinting so alert identity remains metric identity.
  - **Dependent interfaces.** When a rule fires on both an interface and a subinterface of it, only the parent alerts: a physical port carries its VLAN subinterfaces down with it, and one unplugged port must not read as fifteen faults. Parentage follows the `parent.vlan` convention (`eth9.101` → `eth9`, Q-in-Q `eth9.101.200` → `eth9.101`). A subinterface down while its parent is healthy still alerts — a real fault rather than a consequence.
  - **Never in service.** An interface that has never been observed operationally up raises no down alert; a port that was never plugged in reports down forever, and "never worked" is not an incident. Backed by the monotonic `inventory.interfaces.ever_up` flag, set by sync the first time it sees the port up and never cleared — deliberately **not** a MetricsQL lookback window, because with a window a genuinely failed link stops alerting once its last healthy sample ages out, which is worse than the noise it removes. Consequences, accepted: a port that failed before NetInv was installed stays silent until it works once (it is visible as down in inventory, marked *never used*), and the flag is only as current as the sync cadence. An interface absent from inventory is **not** suppressed — silence is the wrong default when the data is missing.

## NOT — Notification

- **FR-NOT-01** Channels: SMTP email (TLS, auth), generic webhook (JSON POST, HMAC-SHA256 signature header, custom headers), Slack (incoming webhook). Multiple named instances per type.
- **FR-NOT-02** Routing policies map (severity, scope, schedule) → channel set; default policy: critical+warning → all configured, info → none.
- **FR-NOT-03** Notifications MUST include: severity, device/interface, metric, value vs threshold, duration, deep link to the explaining graph (G3).
- **FR-NOT-04** Delivery MUST be retried per doc 23 (exponential backoff, max 5) with dead-letter capture; delivery outcomes visible per alert.
- **FR-NOT-05** A channel test-send action MUST exist; channel secrets are write-only like credentials.

## MAP — Weathermap (flagship; UI detail in doc 30 §3)

- **FR-MAP-01** Multiple named maps with per-role visibility; CRUD restricted to Operator+.
- **FR-MAP-02** Editor: add/position device nodes, site/cloud/label nodes; snap grid; pan/zoom; undo/redo (≥50 steps); autosave drafts, explicit publish. Site/cloud/label nodes stand for things NetInv does not poll (an ISP, a customer site, a caption): they carry no device binding, never take a live state, and render muted and dashed so they are not mistaken for a device reporting `unknown`. Any node's map label is editable and does not rename the device behind it (sync owns that, doc 11 §3); removing a node also removes links that would otherwise dangle. Saved documents are validated server-side — node kind, unique ids, device nodes carry a device, links join nodes that exist — so a typo in a hand-written or imported definition (FR-MAP-07) is refused rather than stored and silently skipped by the renderer.
- **FR-MAP-03** Links bind to one or two directed interface endpoints; rendered as split-direction arrows colored by live utilization on the classic scale (0–1-10-25-40-55-70-85-100%); link labels show bps in/out.
- **FR-MAP-04** Device nodes colored by state: up (green), degraded/active warning (amber), down/critical (red), unreachable (grey), unpolled (blue outline).
- **FR-MAP-05** Live data refresh ≤ 30 s via the map-data endpoint (doc 09 §maps); clicking a node/link opens device/interface detail.
- **FR-MAP-06** Auto-suggest: from LLDP/CDP topology, offer discovered adjacencies as one-click link additions.
- **FR-MAP-07** Map definitions export/import as JSON (versioned schema) for backup and AI/programmatic generation.
- **FR-MAP-08** Link utilization divides throughput by a capacity resolved most-specific-first: (1) a capacity set on the link; (2) the A-side interface's `ifSpeed`, correct for physical links; (3) `min()` of the two ends' `inventory.devices.wan_capacity_bps`. Step 3 exists because VPN tunnels report `ifSpeed 0` — verified on UniFi gateways, where a PPPoE interface returns 0 for both `ifSpeed` and `ifHighSpeed`, and the physical port beneath reports the link to the ONT rather than the subscribed service. `wan_capacity_bps` is therefore operator-stated, SNMP being unable to supply it. `min()` because a site-to-site tunnel can only run as fast as the smaller of the two circuits carrying it. When either end's capacity is unknown the link is left **uncolored** rather than falling back to the known end — a half-known bottleneck would overstate capacity and under-report utilization, which is the wrong way to be wrong.

## DASH — Dashboard

- **FR-DASH-01..08** One requirement per panel in PRD §4.2; all panels MUST render from cached aggregates (Redis, ≤30 s staleness) — the dashboard MUST NOT fan out live queries per widget per viewer (doc 05 §caching).
- **FR-DASH-09** Time-series panels support range presets (1h/6h/24h/7d/30d/custom) and honor the retention/downsampling tiers in doc 04.
- **FR-DASH-10** Every panel deep-links: alert → graph, top-N row → device/interface detail, heatmap cell → device detail.

## EXP — Export

- **FR-EXP-01** Inventory CSV and Excel export of the current filtered view (all matching rows, not just the page), streamed server-side; Excel includes typed columns and a metadata sheet (filters, user, timestamp).
- **FR-EXP-02** PDF export (P1): inventory summary and per-site availability report templates.
- **FR-EXP-03** Exports are audit-logged (who, what filter, row count) and rate-limited (doc 20 §9).

## AUD — Audit

- **FR-AUD-01** Audit events cover: authentication (per FR-AUTH-05), every mutating API call (actor, action, resource, before/after diff for config objects), sync events, notification failures, and API errors ≥500.
- **FR-AUD-02** Audit records are append-only; no API mutates or deletes them; retention 12 months online (doc 04), then archived per backup strategy.
- **FR-AUD-03** Audit viewer: filter by actor, action type, resource, time range; export CSV; visible to Admin and Auditor only.

## SET — Settings

- **FR-SET-01** System settings (Admin-only): SMTP config, default polling profile, retention tiers, discovery defaults, UI branding name. All changes audited with before/after.
- **FR-SET-02** Notification channel management per FR-NOT.
- **FR-SET-03** Per-user preferences: theme (light/dark/system), default landing page, timezone (display only; storage is UTC).
- **FR-SET-04** Settings are typed key-value (doc 08 `settings` table) with schema validation server-side.

## PLT — Platform management

- **FR-PLT-01** Sites: CRUD, hierarchy (region → datacenter), site metadata (location, contact).
- **FR-PLT-02** Pollers self-register with a one-time enrollment token, are approved by an Admin, and report heartbeat (30 s), version, queue depth, and poll throughput; UI shows poller fleet health.
- **FR-PLT-03** Connector catalog lists in-tree connectors with vendor, version, supported metric families, and required credentials; a device's connector is selected at onboarding (or auto-matched from sysObjectID during discovery).
- **FR-PLT-04** A poller MUST be assignable to exactly one site; devices poll via their site's poller pool.

## API — General API behavior

- **FR-API-01** All functionality available in the UI MUST be available via the versioned REST API (`/api/v1`); the UI is a pure API client.
- **FR-API-02** List endpoints: cursor pagination (opaque cursor, `limit` ≤ 200), stable sort, `filter` query grammar shared with UI filters.
- **FR-API-03** Mutations accept an `Idempotency-Key` header honored for 24 h.
- **FR-API-04** Errors follow the envelope in doc 23 §5 (machine `code`, human `message`, `trace_id`).
- **FR-API-05** Rate limiting per token per doc 20 §9 with standard `RateLimit-*` headers.
