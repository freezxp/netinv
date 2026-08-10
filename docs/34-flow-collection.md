# 34 — Flow collection (NetFlow / sFlow)

**Status:** draft · **Depends on:** 05, 09, 13, 29 · The `netinv-flow` service:
what it receives, what it stores, what it deliberately throws away, and the
exposure that comes with listening on a socket.

> **Validated against a generated source, not against hardware.** Nothing in the
> reference pilot exports flow: the UniFi gateways do not emit NetFlow natively,
> and probing the sFlow MIB (`1.3.6.1.4.1.14706.1`) returned "No more variables
> left" on the UDM-Pro and "No Such Object" on both an EdgeSwitch 16XG and a
> USW-Lite16. Everything below was exercised by sending real NetFlow v5
> datagrams at the collector and reading the resulting series back out of
> VictoriaMetrics. Treat the v5 decode as working and the rest of §2 as
> unwritten code, not as untested code — see §7.

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

- **`talker`** — one host, counted in whichever direction it appeared. Both ends
  of every flow are counted, so a busy server appears once with its total.
- **`conversation`** — an unordered host pair rendered `A ⇄ B`, so A→B and B→A
  accumulate together. An operator asking what a link carries means the
  exchange, not each half of it separately.
- **`application`** — the *lower* of the two ports, plus protocol. The low port
  is a good proxy for the service: the client's port is high and arbitrary, so
  keying on it would produce one bucket per connection and defeat the whole
  aggregation. Well-known ports get names (`https`, `wireguard`); everything
  else falls back to `tcp/9418`, which is perfectly readable.

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

| Variable | Default | Meaning |
|---|---|---|
| `NETINV_FLOW_ADDR` | `:2055` | UDP listen address |
| `NETINV_FLOW_ALLOW` | *(empty)* | Comma-separated CIDRs or bare addresses |
| `NETINV_VM_URL` | — | VictoriaMetrics write endpoint |

A bare address in `NETINV_FLOW_ALLOW` becomes a `/32` or `/128`, which is what
an operator listing a single exporter means.

## 5. Operating it

The failure this service is most likely to present is **silence**, and silence
has two very different causes: no exporter is configured, or an exporter is
sending something the collector cannot decode. Those look identical from the
outside, so the collector distinguishes them in its log at info level:

- `flow intake` — logged whenever anything was refused by the allow-list or
  could not be decoded, with counts. If you see this, an exporter is pointed at
  NetInv and something is wrong with it.
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

## 7. What this increment does not include

Stated explicitly so nobody reads §2's table as a to-do list that is nearly
finished:

- **NetFlow v9, IPFIX and sFlow are not implemented.** See §2 for why v9/IPFIX
  are a design question rather than a coding one.
- **There is no UI.** The series are queryable, and nothing in the frontend
  displays them yet.
- **No API endpoint fronts these series.** They are read through the existing
  metrics proxy like any other.
- **No validation against a real exporter**, because the pilot has none. This is
  the single most useful thing an outside contributor could report — see
  `/CONTRIBUTING.md` and the hardware-validation ask in Discussions.
