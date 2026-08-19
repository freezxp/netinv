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
| GET/PATCH/DELETE | `/sites/{id}` | ″ | 200, 204, 404, 409 (still referenced) |
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
| PATCH | `/devices/{id}` | `devices:write` | 200, 409 (address in use), 422 |
| POST | `/devices/{id}/retire` · `/enable` · `/disable` | `devices:write` | 200 |
| DELETE | `/devices/{id}` (hard purge) | `devices:admin` | 204, 409 (not retired) |

`GET /interfaces` searches every device's ports by **alias, description or interface name**, case-insensitive substring, returning `{data, total}` with the owning device on each row. It is deliberately not device-scoped: alias is the field operators curate — circuit ids, customer names, the far end of a link — and reading it one device at a time meant "which port is the London circuit on" could only be answered by someone who already knew. All three fields are matched together because whoever labelled the port may have used any of them. Removed interfaces and retired devices are excluded: a search turns up ports you can act on, not history. Utilization is not returned — the caller derives it from the counters, since `speed_bps` is on the row and an unknown speed has to read as unknown rather than as a percentage of nothing. `up=1` excludes interfaces whose last synced oper status is not up — filtered in the database so `total` and paging follow it, and matching the value the list displays. Unknown oper status is *not* treated as up: an interface nobody has ever recorded a status for is not evidence of a working port. There is deliberately no server-side "idle" filter, because traffic lives in the metrics store and there is no column to filter on; the UI applies that to the rows it already has and says how many it removed.

`DELETE /sites/{id}` refuses with 409 while anything still references the site, and the message names **every** blocker at once — devices, retired devices, child sites, enrolled pollers and discovery rules — rather than the first one found. An operator emptying a site works through all of them, and discovering them one failed request at a time is the slow way to do it. Retired devices are called out separately because they are excluded from "managed devices" everywhere else in the product: the count that guarded this originally skipped them while the foreign key did not, so the delete failed regardless and blamed pollers and child sites for a site whose only problem was a device someone had retired. Their message offers **move or purge**, not purge alone — a retired device can be reassigned to another site as easily as a live one, and purging destroys the history it was retired to keep. The default inventory filter hides retired devices, which is why the site-delete dialog links to the site's device list: filter to `retired` there and move them in bulk.

`PATCH /devices/{id}` is a partial update over operator-owned fields. Identity discovered by sync — sysName, sysDescr, serial, model — is not writable; a change there would be overwritten by the next sync anyway. **`mgmt_ip` and `snmp_port` are writable**, because a device gets renumbered or moves off DHCP and NetInv has to follow it; until 2026-08-10 both were silently dropped, so a re-address returned 200 and changed nothing, and the only way to correct one was to delete the device and lose its history. Re-addressing onto an address another device holds is a 409, matching create. `snmp_port` omitted means "leave it"; set to 161 it clears the override so the device follows the default rather than pinning to today's value of it.
| POST | `/devices/{id}/sync` (on-demand) | `devices:write` | 202 `{sync_run_id}` |
| GET | `/devices/{id}/interfaces` | `devices:read` | 200 |
| GET | `/interfaces?q=…&customer=…&up=1&limit=…&offset=…` (fleet-wide search) | `devices:read` | 200 |
| GET | `/reports/bandwidth?q=…&customer=…&group=customer&from=…&to=…&format=csv` | `devices:read` | 200, 422 (backwards window) |
| GET | `/interfaces/customers` (assigned names + counts) | `devices:read` | 200 |
| POST | `/interfaces/tags` (CSV or JSON bulk assignment) | `devices:write` | 200 `{matched,updated,unmatched,ambiguous}` |
**Interface customer and tags** are operator-owned columns that sync never writes. ifAlias is where this information usually lives, and it is the *device's* field: a reprovision or a different engineer overwrites it and the next sync dutifully follows, so reporting a customer's usage off an ifAlias substring works right up until it doesn't. `customer` is a column rather than a tag because it is the axis reports group by; `tags` stay free-form with no vocabulary.

`POST /interfaces/tags` accepts `text/csv` (`device,interface,customer,tags`) or JSON. Both sides of the identity are matched the way a human writes them — device by name or management address, interface by name or ifIndex, case-insensitively — because the list being imported came from a billing system or a spreadsheet, and demanding internal ids would mean looking every row up by hand first, which is the work the import removes. A blank cell leaves a value unchanged and `-` clears it, since a blank cell in a spreadsheet overwhelmingly means "I did not fill this in". Rows matching nothing, or matching more than one interface, are reported rather than guessed at — writing a customer onto the wrong circuit surfaces as a wrong invoice, not an error — and the whole import is one transaction, because a half-applied import cannot be safely re-run.

`group=customer` collapses the report to one row per customer, and the aggregation happens **inside the query**. That is the whole reason it is server-side: a customer's peak is the peak of their *combined* traffic, and adding up per-interface peaks assumes every circuit peaked at the same instant; adding up per-interface 95th percentiles is not a percentile of anything. The expression sums each customer's series with `sum by (dir)` and labels the result, unioned across groups — per-interface selectors unioned rather than combined into `device_id=~"a|b",if_index=~"1|7"`, which matches the cross product and would sweep one customer's port on another customer's device into the total, invisibly, onto an invoice. A group's speed is the sum of its members' only when every member reports one, since a partial denominator reports a customer as more congested than they are. Interfaces with no customer are grouped as untagged rather than dropped — an invoice-shaped report that silently omits what nobody has tagged is how a circuit goes unbilled for a year. Queries are POSTed because a grouped expression names every interface and runs to tens of kilobytes.

The customer filter on `/interfaces` and `/reports/bandwidth` is **exact and case-insensitive**, while the free-text `q` also matches customer as a substring: a billing run must not pick up a different customer whose name merely contains this one, but someone typing a name into the search box means the obvious thing.

`GET /reports/bandwidth` answers what a *set* of interfaces did over a period, where the graphs answer what one is doing now. Selection is the same alias/description search, because the set an operator reports on — "every customer circuit", "all the uplinks" — is defined by what they wrote on the ports, not by which chassis those sit in. Per interface it returns average, **95th percentile** and peak bits/second in each direction, total bytes each way, and utilization of the busier direction (negative = speed unknown, since ifSpeed is missing on plenty of real ports and 0% would read as idle).

Four properties are deliberate. Every query is anchored at the **end of the window**, not at now, because a report about last month must not be evaluated against today. Totals use `increase()` over the whole window rather than summing rates, so a device reboot mid-report does not lose its counter reset. The subquery step grows with the window, capping evaluation at ~720 points — a month at the rate window would be tens of thousands of points per series, for resolution a monthly average does not have. And series arriving for interfaces outside the selection are dropped in the caller rather than filtered in the query, because a selector naming 500 device/index pairs is a regex the store recompiles on every evaluation.

`format=csv` streams the same rows with the window in both the filename and a header line: a bandwidth figure without its period is meaningless, and a spreadsheet outlives the filename. Unknown utilization is an empty cell rather than 0.

| PATCH | `/devices/{id}/interfaces/{ifId}` (`monitor`, notes) | `devices:write` | 200 |
| GET | `/devices/{id}/oids?root=…&limit=…` (live SNMP walk) | `devices:read` | 200, 503 (not configured) |
| GET | `/devices/{id}/components` | `devices:read` | 200 |
| GET | `/devices/{id}/history` (asset history) | `devices:read` | 200 |
| GET | `/devices/{id}/neighbors` (topology) | `devices:read` | 200 |
| GET | `/devices/{id}/alerts` | `alerts:read` | 200 |
| GET | `/device-groups` + CRUD | `devices:read/write` | 200/201/204 |
| GET | `/devices/{id}/sync-runs?limit=…` (sync history + failure reason) | `devices:read` | 200 |

`GET /devices/{id}/sync-runs` replaces the `/sync-runs?device=…` collection this
spec carried until 2026-08-18. It was never built, and the gap had a cost: a
device leaves `pending` only when a sync applies, so a device that cannot sync
sits there indefinitely while ICMP and traffic polls keep succeeding — and the
reason is written to `platform.sync_runs.error` on every attempt. With no
reader, that reason was visible only to someone with a psql prompt. The route
is nested to match every other device sub-resource (`/interfaces`, `/history`,
`/neighbors`), and returns newest-first; `error` is present only on a failed
run and `duration_s` only on a finished one, so an in-flight sync is not
reported as an instant one.

The response also carries a `site` object — `{site_id, known, consumers,
queued, no_consumer_since, checked_at}` — reporting whether anything is
consuming the site's job queue (migration 0013). It rides along rather than
sitting behind its own endpoint because "why is this device pending" has two
halves and the page cannot name the cause holding only one: a sync that failed
leaves a reason on a run row, while a site nobody polls produces *no run at
all*, no failure and no log line. `known` is false when the scheduler has not
dispatched to the site yet; that is reported as unknown rather than as "no
poller", because claiming a fault from missing data is how a diagnostic stops
being believed.

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
| POST | `/maps/generate` (draft from LLDP topology) | `maps:write` | 201 `{id,name,nodes,links}`, 400 (nothing to draw) |

`POST /maps/generate` builds a **draft** from the LLDP adjacencies between managed devices — the same data `/maps/{id}/suggestions` offers one link at a time (FR-MAP-06). Hand-drawing stays the flagship, but the first map is the expensive one: placing every device and binding every link before seeing anything, when the devices already report the topology. Four rules make the output trustworthy rather than merely present:

- **Both LLDP directions collapse into one link.** A↔B is reported twice, and each row carries the ifIndex of *its own* end, so the two rows are merged to bind both endpoints. Treating them as separate links draws the topology twice; keeping only the first leaves one side graphing nothing.
- **An endpoint is bound only when its ifIndex is known.** A guessed binding graphs a real port that is not the link, which is far harder to notice than a link that graphs nothing.
- **Neighbours NetInv does not manage are skipped**, along with self-adjacencies. An unmanaged neighbour has no interfaces to graph, so its link would sit permanently idle and read as a fault.
- **It is deterministic** — same adjacencies, same node ids and positions. Regenerating after the estate changes is normal, and unstable ids would make every diff look like everything moved.

It returns 400 rather than creating an empty map when nothing is drawable, because a map with no nodes is indistinguishable from a broken generator; the message says what to check. The result is never published: what comes out is a truthful picture of what LLDP saw, not a considered diagram.

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

**`if_index` in a saved endpoint is a snapshot, not a binding.** The server stores each link's interface by its stable row id as well, and `GET /maps/{id}/live` resolves the current index from that at render time, falling back to the value in the document only when it no longer resolves. A map saved before a device renumbered therefore keeps working, and a client that reads `if_index` out of a map document to query metrics with will not (doc 30 §3).

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

`GET /settings` → typed map · `PUT /settings/{key}` (schema-validated, audited) · `GET/PUT /users/me/preferences` (any authenticated user).

`/users/me/preferences` stores opaque JSON per user in `iam.user_preferences` — the dashboard layout is its first consumer (doc 30 §2). The body must be a JSON **object** and is capped at 64 KB: a bare array or string would persist happily and then break every client that reads a key from it. `GET` returns `{}` rather than 404 before anything is saved, since first use is a normal state. Clients merge rather than replace, so saving a layout cannot drop a theme.

## 15. Platform meta

### `GET /platform/polling` · `PUT /platform/polling` — fleet-wide collection cadence

`GET` requires `platform:read`; `PUT` requires `platform:write`, because changing the cadence alters SNMP load on every monitored device. Body: `{"traffic_interval_s": 300}`, restricted to `allowed_traffic_interval_s` (60/300/600/900) — 422 otherwise.

Writes the default polling profile **and** the `polling_schedule` rows in one transaction. The scheduler reads the schedule, not the profile, so updating one alone would leave the UI reporting a cadence collection was not using. Health is raised to match when traffic overtakes it; ICMP and inventory sync are untouched. Audited as `platform.polling.update`.

### `GET /metrics/names` — metric names available to chart

Permission `metrics:read`. Lists `netinv_*` series names from the metrics store, sorted. VictoriaMetrics' internal `vm_*` metrics are filtered out. Exists so a dashboard builder need not hard-code a list that goes stale the moment a connector publishes something new.

### `GET /metrics/limits` — query ceiling and poll cadence

Reports `max_range_s` (see §7) and `poll_interval_s`. The cadence travels with the ceiling because clients must size `rate()` lookbacks above it: a lookback shorter than the interval spans at most one sample, `rate()` returns nothing, and every traffic chart goes blank.

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
