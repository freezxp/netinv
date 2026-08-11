# 34 — Flow collection (NetFlow / sFlow)

**Status:** draft · **Depends on:** 05, 09, 13, 29 · The `netinv-flow` service:
what it receives, what it stores, what it deliberately throws away, the
exposure that comes with listening on a socket, and the UI that reads it.

> **Validated against a generated source, not against hardware.** Nothing in the
> reference pilot exports flow: the UniFi gateways do not emit NetFlow natively,
> and probing the sFlow MIB (`1.3.6.1.4.1.14706.1`) returned "No more variables
> left" on the UDM-Pro and "No Such Object" on both an EdgeSwitch 16XG and a
> USW-Lite16. Everything below was exercised by sending real NetFlow v5
> datagrams at the collector and reading the resulting series back out of
> VictoriaMetrics. Treat the v5 decode as working and the rest of §2 as
> unwritten code, not as untested code — see §8.

## 1. Why this service exists, and why it is shaped like this

Interface counters answer *how much* crossed a link. They cannot answer *what it
consisted of*, which is the immediate next question after a utilization alert.
Flow export answers it.

The reason flow was deferred through v1 (ADR-004, category 11) was never that it
is unwanted. It is that flow data is shaped nothing like the rest of the
platform. A flow record is a wide event keyed by source, destination, port and
protocol. Stored as time series, its cardinality is the number of host pairs on
the network — one busy link can produce more distinct series in an hour than the
entire device fleet produces in a year. That is a columnar-store problem, not a
VictoriaMetrics problem.

ADR-020 resolves this by **aggregating at ingest**. The collector reduces every
interval to top-N talkers, conversations and applications per interface, and
writes only those. The cardinality is then `N × interfaces × 3`, which the
existing stack handles without a new datastore.

The cost is stated plainly because it is not recoverable: **a conversation
outside the top N for an interval was never written.** It did not go unqueried —
it does not exist. If you need "every flow from this host last Tuesday", that is
raw per-flow retention, it needs ClickHouse or equivalent, and it needs its own
ADR. This service will not grow into it by accident.

## 2. What it decodes

| Format | Port | Status |
|---|---|---|
| NetFlow v5 | 2055/udp | **Implemented and validated** |
| NetFlow v9 | 2055/udp | Not implemented — needs template state |
| IPFIX | 4739/udp | Not implemented — needs template state |
| sFlow v5 | 6343/udp | Not implemented — needs its own decoder |

v5 came first because it is the one format that decodes without per-exporter
state: fixed 24-byte header, fixed 48-byte records, at most 30 per datagram, no
templates. v9 and IPFIX cannot decode a single packet until the exporter has
sent a matching template, so they need template storage, expiry, and a decision
about what to do with data that arrives before its template — a design question,
not a coding task, and one this increment does not answer.

Unrecognised versions are counted, not logged (§5), so an operator can tell "an
exporter is sending me v9" apart from "nothing is arriving" without reading a
packet capture.

### 2.1 Sampling

NetFlow v5 carries the sampling rate in bytes 22–24 of the header: the top two
bits are the mode, the low fourteen are the interval. Counts are scaled by that
interval at decode time so no downstream caller has to remember to.

Two edge cases are handled explicitly because both produce silently wrong
numbers rather than errors: **mode 0 means no sampling** regardless of what the
interval field says, and **an interval of 0 or 1 means one-for-one**. Multiplying
by a zero interval zeroes every byte count; treating mode 0 as sampled doubles
them. Neither would look like a failure on a chart.

Records carry a `Sampled` flag through to the series as a `sampled="true"`
label, because ADR-020 requires the UI to be able to say when a number is an
extrapolation. **SNMP remains the authority for interface volume**; flow says
what that volume consisted of. Presenting a sampled flow total beside an SNMP
counter as though both were measured the same way is a misrepresentation, not a
rounding difference.

## 3. What it writes

Two metrics, both counters over the aggregation interval:

| Metric | Meaning |
|---|---|
| `netinv_flow_bytes` | Bytes attributed to the bucket in this interval |
| `netinv_flow_packets` | Packets attributed to the bucket in this interval |

Labels:

| Label | Value |
|---|---|
| `exporter` | Source address of the datagram |
| `if_index` | SNMP ifIndex the flow was attributed to |
| `dimension` | `talker`, `conversation` or `application` |
| `value` | The talker/conversation/application itself |
| `sampled` | `"true"` only when the export declared sampling |

`exporter` is the **datagram's source address**, not any field inside it.
NetFlow v5 carries no exporter identity, and a field that a sender controls
would be a poor key for attributing traffic to a device in any case.

The three dimensions:

- **`talker`** — one host, counted in whichever direction it appeared. Both
  ends of every flow are counted, so a busy server appears once with its total.

  That doubling is worth stating because it changes what a share means: the
  talker totals sum to roughly twice the traffic on the link, so a host that is
  one end of everything approaches 50%, not 100%. Shares here are of endpoint
  traffic, not of link traffic. The alternative — counting only one end — would
  make "top talkers" depend on which direction the exporter happened to see.
- **`conversation`** — an unordered host pair rendered `A ⇄ B`, so A→B and B→A
  accumulate together. An operator asking what a link carries means the
  exchange, not each half of it separately.
- **`application`** — whichever side of the exchange is a **recognised**
  service port, falling back to the *lower* port when neither is. Well-known
  ports get names (`https`, `wireguard`); anything else reads `tcp/9418`.

  Recognition has to beat "lower wins", because plenty of services live above
  the ephemeral floor: a WireGuard flow on 51820 loses to a client port of
  28778, and the bucket then keys on the client — one row per connection, which
  is exactly the cardinality blow-up the heuristic exists to prevent. The
  lower-port rule only decides between two unrecognised ports, where it is
  still the better guess.

### 3.1 Interface attribution

A flow is attributed to its **ingress** ifIndex, falling back to egress when
ingress is zero. **A flow with neither is dropped**, deliberately: it would
otherwise accumulate on `if_index="0"`, which renders as a real interface on a
dashboard and invents a busy link that does not exist.

The aggregation interval is one minute, matching the shortest poll cadence, so
flow series and interface counters line up on a chart without either being
interpolated. If you change the fleet poll cadence (doc 09, `PUT
/platform/polling`), this does **not** follow it — flow is bounded by its own
interval, and the two are independent on purpose.

## 4. Configuration

### 4.1 The collector

| Variable | Default | Meaning |
|---|---|---|
| `NETINV_FLOW_ADDR` | `:2055` | UDP listen address |
| `NETINV_FLOW_ALLOW` | *(empty)* | Comma-separated CIDRs or bare addresses |
| `NETINV_VM_URL` | — | VictoriaMetrics write endpoint |

A bare address in `NETINV_FLOW_ALLOW` becomes a `/32` or `/128`, which is what
an operator listing a single exporter means.

The Compose stack publishes `2055/udp` from the `flow` service. If NetInv runs
behind a firewall, that port has to be reachable from each exporter — flow is
pushed to the collector, not polled from it, which is the opposite direction to
everything else NetInv does.

### 4.2 Configuring a device to export NetFlow v5

> **These snippets are also in the product**, on the device Flow tab — expanded
> when the tab is empty, collapsed behind a disclosure once flow is arriving —
> with the collector's address substituted in and a copy button. That is where
> an operator stands when they need this, and a pointer to a file in the source
> repository asks them to leave and go find it. The two copies must be changed
> together; `features/devices/FlowSetupGuide.tsx` says so at the bottom.
>
> **None of the snippets below were run against hardware.** No device in the
> reference pilot can export flow, so these come from vendor documentation and
> are the starting point for a first attempt, not a verified recipe. If you get
> one working — or find it wrong — that is exactly the report the project is
> asking for (`/CONTRIBUTING.md`).

Four things apply on every platform and cause most first-attempt failures:

1. **Force version 5.** Nearly every current platform defaults to v9 or IPFIX.
   NetInv counts those as undecodable and says so in its log (§5), but it will
   not chart them. This is the single most common reason a correctly-pointed
   exporter produces an empty Flow tab.
2. **Set the active timeout to 1 minute.** Defaults are typically 30 minutes:
   a long-lived transfer is then reported once per half hour, as one enormous
   record, and the chart shows a spike surrounded by nothing rather than a
   sustained flow. One minute matches the aggregation interval (§3.1).
3. **Enable it on the interfaces you care about**, in the ingress direction.
   Global export configuration alone collects nothing on most platforms.
4. **v5 is IPv4-only.** There is no IPv6 in the v5 record format at all — a
   dual-stack link will silently report only half its traffic. If your traffic
   is meaningfully v6, v5 is the wrong format and NetInv cannot yet read the
   right one.

**Cisco IOS / IOS-XE**

```
ip flow-export version 5
ip flow-export destination <netinv-host> 2055
ip flow-cache timeout active 1
ip flow-cache timeout inactive 15
!
interface GigabitEthernet0/1
 ip flow ingress
```

**Juniper Junos** — sampling rather than a flow cache, so the sampling rate is
explicit and NetInv will label the results as estimates:

```
set forwarding-options sampling instance NETINV family inet output flow-server <netinv-host> port 2055
set forwarding-options sampling instance NETINV family inet output flow-server <netinv-host> version 5
set forwarding-options sampling instance NETINV input rate 100
set interfaces ge-0/0/0 unit 0 family inet sampling input
```

**Huawei VRP** (NetStream):

```
ip netstream export version 5
ip netstream export host <netinv-host> 2055
ip netstream timeout active 1
interface GigabitEthernet0/0/1
 ip netstream inbound
```

**MikroTik RouterOS**:

```
/ip traffic-flow set enabled=yes active-flow-timeout=1m
/ip traffic-flow target add dst-address=<netinv-host> port=2055 version=5
```

**VyOS**:

```
set system flow-accounting netflow version 5
set system flow-accounting netflow server <netinv-host> port 2055
set system flow-accounting netflow timeout expiry-interval 60
set system flow-accounting interface eth0
```

**Any Linux host or appliance** (pfSense and OPNsense both package softflowd;
this is also the way to get flow off a device whose firmware cannot export it):

```
softflowd -i eth0 -v 5 -t maxlife=60 -n <netinv-host>:2055
```

**FortiGate and Palo Alto cannot do this either**, which is worth stating
plainly because they are the platforms most likely to have flow worth looking
at. FortiOS exports NetFlow v9, IPFIX and sFlow; PAN-OS exports NetFlow v9.
Neither offers v5 at any version, so no configuration on the device makes this
work — the blocker is on NetInv's side (§2), and the interim answer is a
software exporter on a host in the traffic path. ADR-021 records this as the
reason v9 template support moved from "a format we skipped" to the thing
holding the feature back for its best-suited hardware.

**Ubiquiti UniFi gateways cannot do this.** UDM/UCG firmware exposes no NetFlow
export, and none of the switches tested advertise the sFlow MIB. There is no
configuration that makes the pilot fleet export flow, which is why this feature
has never seen a real exporter.

**ZTE ZXR10** is untested and undocumented here; the platform's NetFlow support
varies by model and software train enough that guessing at a snippet would be
worse than saying nothing.

### 4.3 Checking it worked

In order, because each step rules out the one before it:

1. `docker logs netinv-flow-1` — `flow intake` lines mean packets are arriving
   but cannot be used (wrong version, or refused by the allow-list). **No
   output at all means nothing is arriving**: the exporter is not sending, or
   cannot reach the port.
2. `tcpdump -ni any udp port 2055` on the NetInv host, if the log is silent.
   This separates "the network is not delivering it" from "the collector is not
   reading it".
3. The device's **Flow** tab. If it reports flow arriving from an address that
   is not the device's management IP, attribution is the problem, not
   collection — flow keys on the datagram's source address, and a router
   exporting from a loopback will not match. Either change the export source
   address on the device or record the device on the address it exports from.
4. Allow up to two minutes. The collector aggregates for a minute before
   writing, and VictoriaMetrics withholds the most recent 30 seconds from
   instant queries (§5).

## 5. Operating it

The failure this service is most likely to present is **silence**, and silence
has two very different causes: no exporter is configured, or an exporter is
sending something the collector cannot decode. Those look identical from the
outside, so the collector distinguishes them in its log at info level:

- `flow intake` — logged whenever anything was refused by the allow-list or
  could not be decoded, with counts. If you see this, an exporter is pointed at
  NetInv and something is wrong with it. **The counts are per interval, not
  running totals**, so the line stops appearing when the problem stops; a
  cumulative version would re-report one bad packet every minute forever.
- `flow received but nothing aggregated` — packets decoded but produced no
  buckets, which in practice means flows with no usable ifIndex (§3.1).
- `flow key cap reached` (warn) — the per-interval key cap was hit and detail
  is being folded into an `other` bucket. Totals stay honest; detail is gone.

Nothing at all in the log means nothing is arriving. That is a real answer, and
it is the one the pilot gets.

> A note that cost an hour during development, worth having written down: after
> the collector writes, **VictoriaMetrics will not return those samples to an
> instant query for 30 seconds** (`-search.latencyOffset`, which exists to avoid
> serving partial intervals). A "did my write land?" check run immediately after
> a drain returns zero and looks exactly like a broken writer. Wait, or query a
> range with an end time in the past.

## 6. Exposure

**This is the first NetInv component that accepts unsolicited input from the
network.** Everything else — pollers, the API's outbound calls, the ingester —
initiates its own connections. A UDP listener is a materially different posture
and carries three specific problems, each of which has a bound in the code
rather than a note in this document:

1. **Anyone who can reach the port can inject data.** UDP source addresses are
   trivially spoofed and there is no authentication in any flow protocol. A
   sender can therefore attribute fabricated traffic to any exporter address it
   likes. `NETINV_FLOW_ALLOW` restricts accepted sources; it is a filter, not
   authentication, and on an untrusted segment the port should be firewalled to
   the exporters regardless. **Empty means accept from anywhere** — the sane
   default on a management network and the wrong one anywhere else, so the
   service logs a warning at startup when it is unset.

2. **Unbounded key growth.** Distinct flow keys are attacker-controlled: a
   port scan produces one per port, and a spoofed source can produce one per
   packet. The aggregator caps distinct keys per interval (`DefaultMaxKeys`,
   200 000) and folds everything past the cap into an `other` bucket rather
   than either growing without limit or dropping silently — the totals stay
   correct while the detail degrades. Memory is one interval's map and `Drain`
   resets it, so the worst case is bounded in time as well as size.

3. **Declared lengths must not be believed.** A 24-byte datagram claiming 65535
   records would, decoded naively, read far past the buffer. The v5 decoder
   rejects any count above the specification's 30 and re-checks the actual
   length against the declared one before touching a record.

The read buffer is a full 65535 bytes so an oversized datagram is rejected as
malformed rather than silently truncated into a plausible-looking short packet.

## 7. The UI

A **Flow** tab on device detail (doc 30) reads these series directly through the
metrics proxy. Two things about it are worth knowing before changing it:

`netinv_flow_bytes` is **not cumulative**, so `rate()` — correct for every other
metric in this codebase — under-reports it by the lookback factor without
erroring. The frontend reduces with `sum_over_time(...[window]) / window`
instead, and the window never drops below the aggregation interval, since a
shorter one can fall between samples and return nothing at all. `flowRateExpr`
and `flowWindow` in `api/hooks.ts` exist so no caller has to remember either.

The table ranks on **total bytes over the whole selected range**, not on the
latest sample — ranking on the latest would reorder the table every minute on
ordinary jitter. Its rate column is therefore an average across that range, and
is labelled as such: with traffic covering only part of a long window, that
average sits far below the peak on the chart beside it, and an unlabelled
number invites the reader to think one of the two is wrong.

## 8. What this increment does not include

Stated explicitly so nobody reads §2's table as a to-do list that is nearly
finished:

- **NetFlow v9, IPFIX and sFlow are not implemented.** See §2 for why v9/IPFIX
  are a design question rather than a coding one.
- **No API endpoint fronts these series.** The UI reads them through the
  existing metrics proxy like any other metric, which is why no new endpoint
  was needed; there is no server-side flow resource to version or document in
  doc 09.
- **No validation against a real exporter**, because the pilot has none. This is
  the single most useful thing an outside contributor could report — see
  `/CONTRIBUTING.md` and the hardware-validation ask in Discussions.
