# 02 — Product Requirements Document (PRD)

**Status:** draft · **Depends on:** 01, DECISIONS.md

## 1. Problem statement

Network teams running multi-vendor estates lack a single, modern, self-hosted view of device health, traffic, and topology. Existing options force a choice between legacy (Cacti/MRTG: unscalable, no API/RBAC), DIY toolkits (Prometheus+Grafana: no inventory/discovery/weathermap product experience), or costly closed suites. Consequences: outages detected by users instead of monitoring, capacity exhaustion discovered during incidents, audit questions answered by spreadsheet archaeology.

## 2. Product goals & success metrics

| Goal | Metric (v1 target) |
|---|---|
| G1 Single pane of glass | 100% of managed devices from all 5 vendors visible in one dashboard |
| G2 Detect before users do | Link-down / device-down alert delivered < 60 s from occurrence |
| G3 Explain the graph | Alert → relevant graph in exactly 1 click |
| G4 Prevent capacity incidents | Watchlist flags any link sustaining >70% for 24h; zero "surprise full links" |
| G5 Operable by a small team | Fresh install to first polled device < 30 min via Helm |
| G6 Extensible | New vendor connector added without touching core code (proven by 5 in-tree vendors) |
| G7 Auditable | Every login, config change, and sync event queryable for 12 months |

## 3. Personas

- **Nadia — NOC operator (Operations Team).** Watches the wall dashboard on shift. Needs: unambiguous red/amber/green, alert queue she can ack, weathermap to answer "where". Success: resolves or escalates within SLA without opening a CLI.
- **Marcus — Network engineer.** Investigates and plans. Needs: interface error/discard graphs, optic light levels, LLDP topology truth, 12-month history for capacity planning. Success: root-causes a flapping link from NetInv alone.
- **Priya — Service assurance.** Reports availability against SLAs. Needs: per-site availability %, latency/loss trends, PDF/Excel exports, auditor-safe read-only access. Success: monthly SLA report generated in minutes.
- **Tomás — Cloud administrator.** Owns the platform. Needs: Helm install, upgrade path, API tokens for automation, backup/restore runbook. Success: upgrades in a maintenance window without data loss.
- **Management.** Consumes: uptime summary, capacity watchlist, inventory counts for budget cycles. Interacts via exported reports and the read-only dashboard.

## 4. Feature requirements

Priorities: **P0** = v1 must-ship · **P1** = v1 stretch, first post-v1 otherwise · **P2** = roadmap (doc 29).

### 4.1 Collection (P0)
- SNMP v2c and v3 (authPriv: SHA-1/SHA-256 auth, AES-128/256 priv) per-device credentials.
- Metric families: IF-MIB traffic/errors/discards/status (64-bit counters), vendor CPU/memory/temperature/fan/PSU/optics, sysUpTime, ICMP reachability/RTT/jitter/loss, SNMP poll success rate.
- Inventory metadata: sysName/Descr/Location/Contact, model, serial, firmware, interface names/aliases/MTU/speed, LLDP/CDP neighbors.
- Default cadences: traffic counters 60 s, health 300 s, ICMP 30 s, inventory sync 6 h — all per-device configurable.
- Site-local pollers for 4–5 datacenters; outbound-only connectivity.

### 4.2 Dashboard (P0) — panels per the kickoff brief
Status summary row (up/down/unreachable, alerts by severity, rolling 24 h availability %, aggregate throughput now) · Active alerts panel (severity+recency sort, ack state, 1-click to graph) · **Weathermap** (doc 30 §3; flagship) · Top-N lists (utilization, CPU/mem, errors/discards) · Key time-series (aggregate uplink bandwidth 24 h, latency/loss to key targets) · Device health heatmap · Capacity watchlist (sustained >70–80%, trend arrows). Recent events stream is **P1** (limited to NetInv-internal events until trap/syslog ingestion, which is P2).

### 4.3 Inventory (P0)
Search (name/IP/serial/model/location), filtering (vendor/site/group/status/firmware), grouping (site, vendor, device group, custom tags), CSV + Excel export; **PDF export P1**. Asset history: timeline of detected changes (firmware upgraded, serial swapped, interface added, device unreachable) from the sync engine's change detection.

### 4.4 Alerting & notification (P0)
Threshold and state-change rules (MetricsQL-backed), severity levels critical/warning/info, per-rule routing to channels, ack + silence with expiry, flap suppression. Channels: Email (SMTP), generic Webhook, Slack. Maintenance windows **P1**.

### 4.5 Weathermap editor (P0 — flagship)
Canvas with pannable/zoomable map; add device nodes (search-picker) and non-device nodes (sites, clouds, labels); draw links bound to specific device interfaces (one or two-ended); links render live bidirectional utilization with the classic weathermap color scale; nodes colored by device state; background image upload (P1); multiple named maps with RBAC visibility; auto-suggest links from LLDP topology.

### 4.6 Administration (P0)
RBAC roles Admin/Operator/Read-Only/Auditor (matrix in doc 20); user management; audit log viewer (login, config change, sync event, API error); settings: SMTP/webhook/Slack config, polling defaults, data retention, theme (light/dark/system); credential vault UI.

### 4.7 Platform management (P0)
Site (datacenter) management; poller registration/health; connector catalog view (which vendors available, version); device onboarding: manual add, CSV import, and **subnet discovery scan (P1)** with approve-to-manage queue.

### 4.8 Deferred (P2 — designed-in seams)
Wireless (categories 7), firewall/NAT/LB (6), hosts (8), synthetic checks (9), facilities/UPS (10), NetFlow/sFlow (11), syslog/trap events, Keycloak SSO, multi-tenancy activation, HA, mobile app.

## 5. Non-goals (v1)
- No configuration management/push (read-only monitoring; NetInv never writes to devices).
- No packet capture or flow analysis.
- No agent installation on servers (HOST-RESOURCES-MIB polling is P2, agentless).
- No public SaaS signup, billing, or tenant self-service.

## 6. Release criteria (v1.0)
1. All P0 features functional against real devices of ≥3 of the 5 vendors (lab: all 5 via simulator, doc 24).
2. NFR targets met at 500-device simulated load (doc 04).
3. Zero P0/P1 severity open defects; security checklist (doc 20 §12) passed.
4. Helm install + backup/restore runbook validated on a clean cluster.
5. Docs 08/09/10 updated to as-built state (AI-native requirement).

## 7. Open questions
Tracked in DECISIONS.md as `proposed` entries; none currently blocking.
