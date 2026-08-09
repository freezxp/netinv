# 09 — REST API Specification

**Status:** review · **Depends on:** 03 (FR-API), 08, 20, 23

## 1. Conventions (apply to every endpoint)

- Base: `https://<host>/api/v1`. JSON UTF-8. All timestamps RFC 3339 UTC. IDs are ULIDs prefixed by type (`d_`, `if_`, `al_`, `cr_`…).
- **AuthN:** `Authorization: Bearer <access-JWT | api-token>` on everything except `POST /auth/login`, `POST /auth/refresh`, `GET /healthz`. Cookie-carried refresh tokens only touch `/auth/*`.
- **AuthZ:** each endpoint lists its required permission (doc 20 §5 matrix). Missing → `403 {"error":{"code":"forbidden"}}`.
- **Pagination (all lists):** `?limit=` (≤200, default 50) `&cursor=`; response envelope `{"data":[...], "next_cursor":"...|null", "total":<int|null>}` (`total` computed only when `&count=true`).
- **Filtering:** `?filter=` with grammar `field:op:value` comma-AND, e.g. `filter=site:eq:s_dc-east,status:in:active|unreachable,name:contains:core`. `?q=` for full-text (FR-DEV-03). `?sort=field` / `-field`.
- **Mutations:** support `Idempotency-Key` header (FR-API-03). Config mutations are audited automatically.
- **Errors:** envelope per doc 23 §5: `{"error":{"code":"validation_failed","message":"…","details":[…],"trace_id":"…"}}`.
- **Common status codes:** `200` OK · `201` Created (+`Location`) · `202` Accepted (async) · `204` No Content · `400` malformed · `401` unauthenticated · `403` forbidden · `404` not found (or not visible) · `409` conflict/duplicate · `410` cursor expired · `422` validation · `423` locked account · `429` rate limited (+`Retry-After`) · `500`/`503` per doc 23.
- **Rate limits:** per doc 20 §9; headers `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`.
- **Versioning:** `/v1` additive-only post-1.0 (NFR-63). Deprecations: `Deprecation` + `Sunset` headers ≥2 minor versions before removal.

Below, one full worked example per resource family; sibling endpoints share shapes.

## 2. Auth — `/auth`

| Method | URI | Permission | Codes |
|---|---|---|---|
| POST | `/auth/login` | public | 200, 401, 422, 423, 429 |
| POST | `/auth/refresh` | refresh cookie | 200, 401 |
| POST | `/auth/logout` | authenticated | 204 |
| GET | `/auth/me` | authenticated | 200 |
| PUT | `/auth/me/password` | authenticated | 204, 401 (old pw), 422 (policy) |

**POST /auth/login**
```json
// request
{"username": "nadia", "password": "•••"}
// 200 — refresh token set as httpOnly Secure SameSite=Strict cookie
{"access_token": "eyJhbGciOiJFZERTQSIs…", "token_type": "Bearer", "expires_in": 900,
 "user": {"id": "u_01J9…", "username": "nadia", "display_name": "Nadia R.", "roles": ["operator"]}}
// 423
{"error": {"code": "account_locked", "message": "Locked for 15 minutes after failed attempts", "trace_id": "…"}}
```

## 3. Users & roles — `/users`, `/roles` (Admin)

| Method | URI | Permission | Codes |
|---|---|---|---|
| GET | `/users` | `users:read` | 200 |
| POST | `/users` | `users:write` | 201, 409, 422 |
| GET/PATCH | `/users/{id}` | `users:read` / `users:write` | 200, 404 |
| POST | `/users/{id}/deactivate` · `/activate` · `/reset-password` | `users:write` | 200/202 |
| PUT | `/users/{id}/roles` | `users:write` | 200, 422 |
| GET | `/roles` | `users:read` | 200 |
| GET/POST | `/users/me/tokens` (API tokens) | authenticated | 200 / 201 (token value shown once) |
| DELETE | `/users/me/tokens/{id}` | authenticated | 204 |

**POST /users** → `{"username","email","display_name","roles":["readonly"],"temporary_password"}` → 201 user object (never includes hash).

## 4. Sites & pollers — `/sites`, `/pollers`

| Method | URI | Permission | Codes |
|---|---|---|---|
| GET/POST | `/sites` | `platform:read` / `platform:write` | 200/201 |
| GET/PATCH/DELETE | `/sites/{id}` | ″ | 200, 204, 404, 409 (has devices) |
| GET | `/pollers` | `platform:read` | 200 |
| POST | `/pollers/enroll-tokens` | `platform:write` | 201 `{token, expires_at}` (once) |
| POST | `/pollers/register` | enroll token | 201 (called by poller itself) |
| POST | `/pollers/{id}/approve` · `/disable` | `platform:write` | 200 |
| GET | `/pollers/{id}` | `platform:read` | 200 — includes heartbeat, version, throughput |

## 5. Credentials — `/credentials`

| Method | URI | Permission | Codes |
|---|---|---|---|
| GET | `/credentials` | `credentials:read` (Admin only) | 200 — metadata only |
| POST | `/credentials` | `credentials:write` | 201, 422 |
| PATCH | `/credentials/{id}` | `credentials:write` | 200 (secret fields write-only) |
| DELETE | `/credentials/{id}` | `credentials:write` | 204, 409 `credential_in_use` |
| POST | `/credentials/{id}/test` | `credentials:write` | 200 |

**POST /credentials**
```json
{"name": "core-v3", "kind": "snmp_v3",
 "secret": {"username": "netinv", "auth_protocol": "sha256", "auth_password": "•••",
            "priv_protocol": "aes256", "priv_password": "•••", "context": ""}}
// 201 — secret never returned again
{"id": "cr_01J9…", "name": "core-v3", "kind": "snmp_v3",
 "meta": {"username": "netinv", "auth_protocol": "sha256", "priv_protocol": "aes256"},
 "device_count": 0, "created_at": "2026-08-07T09:00:00Z"}
```
**POST /credentials/{id}/test** `{"target_ip":"10.0.1.1","port":161}` → 200 `{"result":"ok","sys_name":"core-sw-1","latency_ms":12}` or `{"result":"auth_failure"|"timeout"|"priv_failure"}`.

## 6. Devices & inventory — `/devices`

| Method | URI | Permission | Codes |
|---|---|---|---|
| GET | `/devices` | `devices:read` | 200 |
| POST | `/devices` | `devices:write` | 201, 409 (duplicate IP), 422 |
| POST | `/devices/import` (CSV multipart) | `devices:write` | 200 row-report |
| GET | `/devices/{id}` | `devices:read` | 200, 404 |
| PATCH | `/devices/{id}` | `devices:write` | 200 |
| POST | `/devices/{id}/retire` · `/enable` · `/disable` | `devices:write` | 200 |
| DELETE | `/devices/{id}` (hard purge) | `devices:admin` | 204, 409 (not retired) |
| POST | `/devices/{id}/sync` (on-demand) | `devices:write` | 202 `{sync_run_id}` |
| GET | `/devices/{id}/interfaces` | `devices:read` | 200 |
| PATCH | `/devices/{id}/interfaces/{ifId}` (`monitor`, notes) | `devices:write` | 200 |
| GET | `/devices/{id}/oids?root=…&limit=…` (live SNMP walk) | `devices:read` | 200, 503 (not configured) |
| GET | `/devices/{id}/components` | `devices:read` | 200 |
| GET | `/devices/{id}/history` (asset history) | `devices:read` | 200 |
| GET | `/devices/{id}/neighbors` (topology) | `devices:read` | 200 |
| GET | `/devices/{id}/alerts` | `alerts:read` | 200 |
| GET | `/device-groups` + CRUD | `devices:read/write` | 200/201/204 |
| GET | `/sync-runs?device=…` | `devices:read` | 200 |

**GET /devices?filter=site:eq:s_dceast,status:eq:active&q=core&sort=-updated_at&limit=2**
```json
{"data": [{
   "id": "d_01J9AB…", "name": "core-sw-1", "mgmt_ip": "10.0.1.1",
   "site": {"id": "s_dceast", "name": "DC East"},
   "connector_id": "juniper-junos", "credential_id": "cr_01J9…", "profile_id": "pp_default",
   "status": "active", "vendor": "Juniper", "model": "QFX5120-48Y",
   "serial_number": "WS012345", "os_version": "23.4R2",
   "sys_name": "core-sw-1.dceast", "sys_location": "DC-East row 4",
   "interface_count": 52, "active_alert_count": 1,
   "tags": ["core", "prod"], "wan_capacity_bps": 500000000,
   "created_at": "…", "updated_at": "…"}],
 "next_cursor": "g2wAAAAB…", "total": null}
```

`wan_capacity_bps` is the subscribed uplink rate in bits/s, `0` when nobody has stated it; PATCH accepts `0` to clear it (FR-MAP-08). `GET /devices/{id}/interfaces` rows carry `ever_up` — false for a port never observed operationally up, which is excluded from interface-down alerting (FR-ALR-08) and labelled as such in the UI so its silence is explainable. The OID walk runs live from the API against the device and is audited (`device.oid_walk`); it is the tool for answering "what does this platform actually support?" before writing a connector.

## 7. Metrics query — `/metrics` (proxy to VictoriaMetrics, scope-guarded)

| Method | URI | Permission | Codes |
|---|---|---|---|
| GET | `/metrics/query?query=…&time=…` | `metrics:read` | 200, 422 (expr rejected) |
| GET | `/metrics/query_range?query=…&start=…&end=…&step=…` | `metrics:read` | 200, 422 (range/step caps) |
| GET | `/devices/{id}/metrics/{metric}?range=24h&step=60s` | `metrics:read` | 200 (convenience wrapper) |
| GET | `/metrics/catalog` | `metrics:read` | 200 — metric names, units, families |

Response = Prometheus API shape (`{"status":"success","data":{"resultType":"matrix","result":[…]}}`) so any PromQL-ecosystem client works. Server limits: range ≤ `NETINV_VM_RETENTION` (default 2 y, and the same value VictoriaMetrics is started with, so the ceiling cannot fall below the data actually kept), step ≥ raw resolution, series ≤ 5k per query. Over-long ranges are **rejected** with 422, not clamped — a silently shortened window would make a chart lie about the period it covers. `GET /metrics/limits` reports the ceiling as `max_range_s`, so a client can offer ranges that will actually resolve instead of guessing.

## 8. Dashboard — `/dashboard` (cached aggregates, doc 05 §7)

| Method | URI | Permission |
|---|---|---|
| GET | `/dashboard/summary` | `metrics:read` — up/down/unreachable, alert counts, availability 24h, aggregate bps |
| GET | `/dashboard/top?list=if_utilization\|cpu\|memory\|if_errors&n=10` | `metrics:read` |
| GET | `/dashboard/heatmap` | `metrics:read` — device cells, worst-metric class |
| GET | `/dashboard/watchlist` | `metrics:read` — links >70% sustained + trend |
| GET | `/dashboard/events?limit=50` | `metrics:read` — internal event stream (P1) |

**GET /dashboard/summary** → `{"devices":{"up":471,"down":3,"unreachable":6,"disabled":20},"alerts":{"critical":2,"warning":11,"info":4},"availability_24h":99.72,"throughput_bps":{"in":182000000000,"out":179000000000},"as_of":"2026-08-07T09:12:30Z"}`

## 9. Alerts — `/alerts`, `/alert-rules`, `/silences`

| Method | URI | Permission | Codes |
|---|---|---|---|
| GET | `/alerts?filter=state:in:firing|acknowledged&sort=-severity,-fired_at` | `alerts:read` | 200 |
| GET | `/alerts/{id}` (incl. events timeline, deliveries) | `alerts:read` | 200 |
| POST | `/alerts/{id}/ack` `{"comment":"…"}` | `alerts:ack` | 200, 409 (already resolved) |
| POST | `/alerts/{id}/unack` | `alerts:ack` | 200 |
| GET/POST | `/alert-rules` | `alerts:read` / `alerts:admin` | 200/201 |
| GET/PATCH/DELETE | `/alert-rules/{id}` | ″ | 200/204; **409 on DELETE of a builtin** |
| POST | `/alert-rules/{id}/enable` · `/disable` | `alerts:admin` | 204 |
| POST | `/alert-rules/{id}/preview` (dry-run expr) | `alerts:admin` | *not implemented* — expressions are instead validated against the metrics backend on create/update, which catches the case that matters (a rule stored with an unparseable expression never fires and says nothing) |
| GET/POST | `/silences` · DELETE `/silences/{id}` | `alerts:read` / `alerts:ack` | 200/201/204 |

Built-in rules (`is_builtin`, the FR-ALR-07 pack) are **fully editable** — thresholds and severity are exactly what an operator needs to tune — and disable-able, but not deletable: nothing short of a re-migration would restore one, and their ids appear in docs and runbooks. Deleting an operator's own rule takes its alert history with it, since `alert_instances` cannot be orphaned; the count is reported in the rule list as `firing` so a caller can warn first. Every mutation is audited with before/after, **including enable/disable** — that one silences monitoring (FR-ALR-09).

**Alert object**
```json
{"id": "al_01J9…", "rule": {"id": "ar_util90", "name": "Interface >90% util"},
 "state": "firing", "severity": "critical",
 "device": {"id": "d_01J9AB…", "name": "core-sw-1"},
 "interface": {"id": "if_01J9…", "name": "xe-0/0/1", "alias": "uplink-dc-west"},
 "value": 93.4, "threshold": 90, "fired_at": "2026-08-07T08:55:00Z", "duration_s": 1050,
 "acked": null, "graph_url": "/devices/d_01J9AB…?if=12&metric=utilization&from=-3h",
 "labels": {"site": "dc-east"}}
```

## 10. Weathermaps — `/maps`

| Method | URI | Permission | Codes |
|---|---|---|---|
| GET | `/maps` | `maps:read` | 200 (filtered by role floor) |
| POST | `/maps` | `maps:write` | 201 |
| GET | `/maps/{id}` (`?rev=draft`) | `maps:read` | 200 |
| PUT | `/maps/{id}/draft` (full definition autosave) | `maps:write` | 200, 409 (stale base rev) |
| POST | `/maps/{id}/publish` | `maps:write` | 200, 422 (broken bindings) |
| GET | `/maps/{id}/live` | `maps:read` | 200 (≤30 s cache) |
| GET | `/maps/{id}/suggestions` (LLDP link candidates) | `maps:write` | 200 |
| GET | `/maps/{id}/export` · POST `/maps/import` | `maps:read` / `maps:write` | 200/201 |
| DELETE | `/maps/{id}` | `maps:write` | 204 |

**Map definition (stored/exported JSON, `"schema":"netinv.map/1"`)**
```json
{"schema": "netinv.map/1", "name": "Backbone",
 "options": {"grid": 10, "util_scale": [1,10,25,40,55,70,85,100]},
 "nodes": [
   {"id": "n1", "kind": "device", "device_id": "d_01J9AB…", "x": 120, "y": 80, "label": "auto"},
   {"id": "n2", "kind": "site",   "site_id": "s_dcwest", "x": 520, "y": 80, "label": "DC West"},
   {"id": "n3", "kind": "label",  "x": 320, "y": 20, "text": "Backbone 2026"}],
 "links": [
   {"id": "l1", "from": "n1", "to": "n2", "width": 4,
    "from_handle": "r", "to_handle": "l",
    "a_endpoint": {"device_id": "d_01J9AB…", "if_index": 12},
    "b_endpoint": {"device_id": "d_01J9CD…", "if_index": 3},
    "bandwidth_bps": 10000000000, "style": "curved"}]}
```
`from_handle`/`to_handle` (`t|r|b|l`) record which side of each node the operator drew the line from, so a map redraws as it was arranged; both are cosmetic and optional. Node `kind` is one of `device|site|cloud|label`; only `device` carries a live state, the rest stand for things NetInv does not poll (FR-MAP-02). Saved definitions are validated server-side — known kinds, unique ids, device nodes carrying a device, links joining nodes that exist — because the renderer silently skips anything malformed.

**One bound endpoint is enough.** Rates come from `a_endpoint` when it is bound and from `b_endpoint` otherwise, mirrored so both stay relative to the link's A side. A link to something unpollable has only one real interface, and which slot it lands in depends only on the direction it was drawn (FR-MAP-03).
**GET /maps/{id}/live** → `{"as_of":"…","nodes":[{"id":"n1","state":"up","worst_alert":"warning"}],"links":[{"id":"l1","in_bps":8.1e9,"out_bps":2.3e9,"util_in":81.0,"util_out":23.0,"capacity_bps":1.0e10,"state":"up"}]}`

`capacity_bps` is whatever utilization was divided by, `0` when unknown — sent so a client can tell an idle link from one with no capacity to measure against, since both otherwise read 0%. Resolution order is link `bandwidth_bps` → A-side `ifSpeed` → `min()` of the two ends' `wan_capacity_bps` (FR-MAP-08). `nodes` and `links` are always arrays, never `null`.

## 11. Notifications — `/notification-channels`, `/notification-policies`

CRUD both (Admin `settings:write`); `POST /notification-channels/{id}/test` → 200 delivery result; `GET /alerts/{id}/deliveries` under alerts. Channel secrets write-only like credentials.

## 12. Audit — `/audit-events` (Admin, Auditor: `audit:read`)

`GET /audit-events?filter=actor:eq:u_…,action:prefix:device.,at:gte:2026-08-01T00:00:00Z` → 200 audit rows (doc 08 shape) · `GET /audit-events/export` → CSV stream (rate-limited).

## 13. Exports — `/exports`

| Method | URI | Permission | Codes |
|---|---|---|---|
| POST | `/exports/inventory` `{"format":"csv|xlsx|pdf","filter":"…","columns":[…]}` | `devices:read` | 202 `{export_id}` |
| GET | `/exports/{id}` | owner | 200 `{status, download_url?}` |
| GET | `/exports/{id}/download` | owner | 200 (stream), 404, 410 (expired 24 h) |

Async job (RabbitMQ worker in API deployment); audit-logged per FR-EXP-03.

## 14. Settings — `/settings` (Admin)

`GET /settings` → typed map · `PUT /settings/{key}` (schema-validated, audited) · `GET/PUT /users/me/preferences` (theme etc., any authenticated user).

## 15. Platform meta

### `GET /platform/capacity` — storage capacity and retention headroom

Permission `platform:read`. Reports what the metrics store holds and how long the volume can sustain it. Every value is measured from the running system rather than read from configuration.

| Field | Meaning |
|---|---|
| `retention_s` | Configured retention (`NETINV_VM_RETENTION`) |
| `disk.used_bytes` / `disk.free_bytes` | Metrics data size, and free space on its volume as VictoriaMetrics reports it |
| `metrics.bytes_per_sample` | Measured compression — around 0.8 in practice |
| `metrics.samples_per_day`, `metrics.effective_interval_s` | Write rate, counted from the last hour of stored data |
| `growth.bytes_per_day`, `growth.bytes_per_device_per_year` | Growth; the per-device figure is the one to plan a fleet with |
| `growth.days_until_full` | At the current rate; `-1` when not yet measurable |
| `growth.max_retention_s` | What the whole volume could sustain, as opposed to what is configured |
| `warnings` | Plain-language problems, most serious first. Always an array, never null |

Degrades rather than fails: if VictoriaMetrics answers `/metrics` but not queries, the disk figures are still returned and the projections read as unknown.

`GET /connectors` (`platform:read`) → catalog with capabilities · `GET /system/info` → version, build, component health summary · `GET /healthz`, `/readyz` (public, no auth, no detail) · `GET /metrics` (Prometheus, cluster-internal only — doc 22).

## 16. Discovery (P1) — `/discovery`

`GET/POST /discovery/rules` · `POST /discovery/rules/{id}/run` → 202 · `GET /discovery/found?state=pending` · `POST /discovery/found/{id}/approve` `{site_id, profile_id, name?}` → 201 device · `POST /discovery/found/{id}/ignore`.

---

**Endpoint count:** ~85. OpenAPI 3.1 file will be generated from code annotations in Sprint 3 and kept at `backend/api/openapi.yaml`; this document remains the human/AI-readable contract and the OpenAPI file must match it (CI drift check, doc 25).
