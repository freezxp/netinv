# 34 — Flow collection (NetFlow / sFlow)

**Status:** draft · **Depends on:** 05, 09, 13, 29 · The `netinv-flow` service:
what it receives, what it stores, what it deliberately throws away, the
exposure that comes with listening on a socket, and the UI that reads it.

> **Decoding validated against real hardware (2026-08-12); volumes are not.**
> A UniFi gateway in the reference pilot exports NetFlow v5 to the collector and
> it decodes cleanly — series appear, records parse, the sampled flag is set,
> and the exporter address matched a managed device without intervention. Not a
> single packet was refused or undecodable.
>
> **What is not yet established is whether the numbers are right.** Summed over
> an hour, the flow totals for two interfaces came to roughly 0.06% of what the
> SNMP counters recorded for the same interfaces over the same window. At least
> three things contribute — sparse exports (7 minutes of data in 45), the
> top-N cut discarding everything outside the busiest ten buckets, and possibly
> a declared sampling interval that does not match the rate configured on the
> device — and until they are separated, treat flow here as showing *shape*
> rather than *volume*. SNMP remains the authority (§2.2), which is the reason
> that rule exists. §4.2 records the rest.
>
> An earlier version of this document stated that UniFi gateways cannot export
> flow. **That was wrong** — the feature is in the UniFi Network application
> under Traffic Logging, offering v5, v9 and IPFIX. The claim was never tested,
> only inferred from the absence of an sFlow MIB, and it stood here for two
> days. Recorded rather than quietly deleted, because a confident wrong claim in
> a document is worse than a gap and this one would have stopped an operator
> from trying.
>
> v9, IPFIX, IPv6 flows and options-record sampling remain validated against a
> generated source only — the RFCs' behaviour, not a given firmware's. §8 lists
> what is still unwritten.

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
| NetFlow v5 | 2055/udp | **Implemented** |
| NetFlow v9 | 2055/udp | **Implemented**, including options templates and sampling (§2.1–2.2) |
| IPFIX | 4739/udp | **Implemented**, including variable-length and enterprise fields (§2.1) |
| sFlow v5 | 6343/udp | Not implemented — samples packet headers rather than exporting flow records, so none of this machinery transfers |

The collector listens on **both 2055 and 4739** and reads the version from the
datagram, so any of the three formats works on either port. An exporter that
cannot be told which port to use will use its convention, and meeting both is
cheaper than making every operator translate.

v5 came first because it is the one format that decodes without per-exporter
state: fixed 24-byte header, fixed 48-byte records, at most 30 per datagram, no
templates. v9 followed because that is where the hardware is — the two firewall
platforms added in ADR-021 cannot export v5 at all — and because v5 is IPv4-only,
so a dual-stack link was silently half-reported. IPFIX came with it: once the
template machinery existed, IPFIX was that machinery plus a different header and
a handful of encodings (§2.1).

Unrecognised versions are counted, not logged (§5), so an operator can tell "an
exporter is sending me v9" apart from "nothing is arriving" without reading a
packet capture.

### 2.1 v9 and template state

v9 is not self-describing. An exporter first sends a **template** naming the
fields it will use and their widths; every later data record is an opaque byte
run that means nothing without it. Field widths are the exporter's choice —
`INPUT_SNMP` is 2 bytes on one platform and 4 on another — so nothing may assume
a size, and fields the decoder does not recognise are skipped by their declared
length rather than breaking the record.

That single difference turns a pure function into a stateful component, with
four consequences worth knowing before touching it (ADR-022):

- **A restart loses every template.** Exporters resend on their own schedule,
  commonly every 10-20 minutes, so flow can be missing for that long after a
  restart while nothing is wrong. Data arriving ahead of its template is
  counted as `awaiting_template` and reported separately from undecodable
  packets — filing it as "malformed" would send an operator hunting a fault
  that does not exist.
- **A packet can carry templates and data at once**, so decoding is not
  all-or-nothing: whatever can be read is returned, and the rest is counted as
  awaiting. Discarding the whole packet would throw away usable flows during
  exactly the window where flow is scarcest.
- **Templates are attacker-influenced state on an unauthenticated port.** A
  spoofed source can mint a new `(exporter, observation domain, template ID)`
  per packet, so the cache is capped at 10 000 entries and expires them after
  60 minutes. Full of *live* templates, it refuses new ones rather than evicting
  a working one.
- **A template may be redefined under the same ID.** The newest definition wins,
  which is what exporters do after a configuration change; holding the old
  layout would misread every subsequent record while looking perfectly healthy.

v9 also carries IPv6, which v5 cannot express at all — the reason a dual-stack
link was previously reported at roughly half its real volume with no indication
that anything was missing.

**IPFIX is that model standardised**, and shares the cache, the record walker
and the field mapping. What it does not share is where a decoder treating it as
"v9 with a different version number" reads the wrong bytes without failing:

- The header is **16 bytes, not 20**, and carries the message's **total length**
  where v9 carries a **record count**. Trusting the v9 field walks past the end
  or stops early. The declared length also bounds the message, so trailing bytes
  added by a middlebox are ignored rather than parsed.
- Template sets are **ID 2 and 3**, where v9 uses 0 and 1.
- An options template declares a scope **field count**; v9 declares scope and
  option **byte lengths**. Same idea, same 16-bit field, same position,
  different unit.
- Fields may be **enterprise-specific**: the high bit of the element ID means a
  4-byte enterprise number follows *in the template*. Miss it and every
  subsequent field is offset by four bytes — which still parses, and produces
  confident nonsense. Those fields are vendor-private with no registry-wide
  meaning, so they are skipped by length rather than interpreted.
- A field may be **variable-length**, with its width in the record rather than
  the template. Such a record cannot be indexed by multiplication; it has to be
  walked, and a walker that assumed otherwise slides off the field boundary and
  reads every later record as garbage that still parses.

v9 and IPFIX number their templates independently, so the cache key includes the
version: an exporter running both can legitimately use ID 256 for two different
layouts, and sharing one key would let each overwrite the other.

### 2.2 Sampling

NetFlow v5 carries the sampling rate in bytes 22–24 of the header: the top two
bits are the mode, the low fourteen are the interval. Counts are scaled by that
interval at decode time so no downstream caller has to remember to.

Two edge cases are handled explicitly because both produce silently wrong
numbers rather than errors: **mode 0 means no sampling** regardless of what the
interval field says, and **an interval of 0 or 1 means one-for-one**. Multiplying
by a zero interval zeroes every byte count; treating mode 0 as sampled doubles
them. Neither would look like a failure on a chart.

On v9 the rate usually arrives in an **options data record** describing the
sampler, not on the flow record itself. A decoder that skipped options
templates would therefore under-report by the whole sampling rate with nothing
anywhere signalling a problem, so options templates are parsed and the interval
they announce is applied per exporter and observation domain. An interval
carried inline on a flow record is honoured too, and takes precedence for that
record.

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
| `NETINV_FLOW_ADDR` | `:2055,:4739` | Comma-separated UDP listen addresses |
| `NETINV_FLOW_ALLOW` | *(empty)* | Comma-separated CIDRs or bare addresses |
| `NETINV_VM_URL` | — | VictoriaMetrics write endpoint |

A bare address in `NETINV_FLOW_ALLOW` becomes a `/32` or `/128`, which is what
an operator listing a single exporter means.

The Compose stack publishes `2055/udp` and `4739/udp` from the `flow` service.
Every listed address must bind or the service fails to start: a partial bind
would leave the collector listening on some ports and silently deaf on others,
which looks exactly like an exporter that is not sending. If NetInv runs
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

1. **Send NetFlow or IPFIX, not sFlow.** v5, v9 and IPFIX are all decoded;
   sFlow is not, and an exporter sending it looks exactly like one sending
   nothing (§5 tells the two apart). Prefer **v9 or IPFIX** — they are
   equivalent here, and both carry IPv6 where v5 cannot.
2. **Set the active timeout to 1 minute.** Defaults are typically 30 minutes:
   a long-lived transfer is then reported once per half hour, as one enormous
   record, and the chart shows a spike surrounded by nothing rather than a
   sustained flow. One minute matches the aggregation interval (§3.1).
3. **Enable it on the interfaces you care about**, in the ingress direction.
   Global export configuration alone collects nothing on most platforms.
4. **Prefer v9 or IPFIX on a dual-stack link.** The v5 record format has no
   IPv6 fields at all, so a v5 exporter on a dual-stack link silently reports
   only half its traffic.
5. **After a NetInv restart, v9 and IPFIX go quiet until templates are
   resent** — commonly 10-20 minutes, and 30 on some defaults. That is the
   protocol working, not a fault; the collector reports it as
   `awaiting_template` (§5). Lowering the exporter's template refresh interval
   shortens the gap. v5 has no such window.

**Cisco IOS / IOS-XE**

```
ip flow-export version 9
ip flow-export destination <netinv-host> 2055
ip flow-export template refresh-rate 30
ip flow-cache timeout active 1
ip flow-cache timeout inactive 15
!
interface GigabitEthernet0/1
 ip flow ingress
```

Substitute `version 5` and drop the refresh-rate line for v5. IPFIX on IOS-XE
means Flexible NetFlow with a custom record and exporter, which is a longer
configuration than this.

**Juniper Junos** — sampling rather than a flow cache, so the sampling rate is
explicit and NetInv will label the results as estimates:

```
set forwarding-options sampling instance NETINV family inet output flow-server <netinv-host> port 2055
set forwarding-options sampling instance NETINV family inet output flow-server <netinv-host> version9 template NETINV-V4
set services flow-monitoring version9 template NETINV-V4 ipv4-template
set services flow-monitoring version9 template NETINV-V4 template-refresh-rate seconds 60
set forwarding-options sampling instance NETINV input rate 100
set interfaces ge-0/0/0 unit 0 family inet sampling input
```

**Huawei VRP** (NetStream):

```
ip netstream export version 9
ip netstream export host <netinv-host> 2055
ip netstream timeout active 1
interface GigabitEthernet0/0/1
 ip netstream inbound
```

**MikroTik RouterOS**:

```
/ip traffic-flow set enabled=yes active-flow-timeout=1m
/ip traffic-flow target add dst-address=<netinv-host> port=2055 version=9
```

**VyOS**:

```
set system flow-accounting netflow version 9
set system flow-accounting netflow server <netinv-host> port 2055
set system flow-accounting netflow timeout expiry-interval 60
set system flow-accounting interface eth0
```

**Any Linux host or appliance** (pfSense and OPNsense both package softflowd;
this is also the way to get flow off a device whose firmware cannot export it):

```
softflowd -i eth0 -v 9 -t maxlife=60 -n <netinv-host>:2055
```

**Fortinet FortiGate** (FortiOS exports v9):

```
config system netflow
    set collector-ip <netinv-host>
    set collector-port 2055
    set active-flow-timeout 60
end
config system interface
    edit "port1"
        set netflow-sampler both
    next
end
```

The per-interface `netflow-sampler` is the step that is easy to miss: the
global block alone configures a collector and exports nothing.

**Palo Alto PAN-OS** (exports v9) is configured in the GUI rather than usefully
in one CLI line: **Device → Server Profiles → NetFlow**, add a server at
`<netinv-host>:2055`, set the template refresh to something short (PAN-OS
defaults to 30 minutes, which is also how long flow stays missing after a NetInv
restart), then assign that profile to each ingress interface under **Network →
Interfaces → *interface* → Advanced → NetFlow Profile**. Assigning the profile
to the interface is the equivalent of FortiOS's per-interface sampler, and the
same thing to forget.

**Ubiquiti UniFi** — validated on a real gateway. In the UniFi Network
application: **Settings → Traffic Logging → NetFlow (IPFIX)**. Tick the networks
to export, set **Collector Address** to the NetInv host and **Port** to 2055
(or 4739 if you pick IPFIX), and choose a **Version** — the panel is labelled
"NetFlow (IPFIX)" but offers 10, 9 and 5, so the heading is the feature's name
rather than the format in use.

Four things about that panel are worth knowing, all learned by running it:

- **Export is per network, not per interface.** Only the networks ticked at the
  top are exported. A network left unticked contributes nothing and looks
  exactly like a quiet one — this is the UniFi equivalent of forgetting the
  per-interface sampler on FortiOS.
- **Sampling defaults to on.** The pilot's gateway was set to Hash mode at
  1-in-512, so every byte count NetInv receives is an extrapolation ×512. The
  header carries the rate, NetInv scales by it and marks the series
  `sampled="true"`, and the Flow tab says so — but treat the totals as
  estimates and keep SNMP as the authority for volume (§2.2). Set **Sampling
  Mode → Off** if you want counted rather than estimated bytes.
- **Timeout Rate is the active timeout**, in minutes, and 5 is the default. That
  is why exports arrive as a burst every five minutes rather than continuously,
  which charts as spikes separated by empty buckets. Lower it if the panel lets
  you.
- **Refresh Rate** is the v9/IPFIX template refresh, expressed in *packets*
  rather than minutes. It greys out on v5, which has no templates.

The switches remain a separate matter: none tested advertise the sFlow MIB, and
this panel is a gateway feature.

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

- **sFlow is not implemented.** It samples packet headers rather than exporting
  flow records, so none of the template machinery here transfers — it needs a
  decoder of its own.
- **No IPv6 exporter transport.** Flow *about* IPv6 traffic works (v9 carries
  it); the collector still listens on UDP over IPv4 only.
- **Template state is not persisted across a restart.** It could be, and that
  would remove the 10-20 minute gap after a restart, but it means writing
  attacker-influenced state to disk — see ADR-022 before deciding that is worth
  it.
- **No API endpoint fronts these series.** The UI reads them through the
  existing metrics proxy like any other metric, which is why no new endpoint
  was needed; there is no server-side flow resource to version or document in
  doc 09.
- **Only one real exporter, on one platform, in one format.** A UniFi gateway
  exporting v5 is now validated end to end (§4.2). v9, IPFIX, IPv6 and
  options-record sampling have been exercised only against a generated source,
  and no Cisco, Juniper, Huawei, FortiGate or PAN-OS exporter has ever reached
  this collector. Reports from those remain the most useful thing an outside
  contributor could send — see `/CONTRIBUTING.md` and the hardware-validation
  ask in Discussions.
