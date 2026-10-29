# 34 — Flow collection (NetFlow / sFlow)

**Status:** draft · **Depends on:** 05, 08, 09, 13, 29 · The `netinv-flow` service:
what it receives, what it stores, what it deliberately throws away, the
exposure that comes with listening on a socket, and the UI that reads it.

> **Two stores since ADR-026 (2026-10-29).** The top-N aggregates in
> VictoriaMetrics (§1, §3) are unchanged and still feed the device Flow tab,
> the dashboard panel, the stale-exporter alerts and `/flow/top|series|exporters`.
> Alongside them, every decoded flow is now written to **ClickHouse** (§7) and
> read by the **Flows** page (§8). §1's reasoning about cardinality explains why
> the aggregates exist; it no longer means NetInv keeps nothing else. The raw
> store, the extra fields (§2.4), sFlow (§2.3) and enrichment (§7.4) are
> validated against a local generator only, like v9 and IPFIX before them.

> **Validated against real hardware (2026-08-12).** UniFi gateways in the
> reference pilot export NetFlow v5 to the collector and it decodes cleanly:
> series appear, records parse, exporter addresses match managed devices, and
> not one packet has been refused or counted undecodable.
>
> **Sampling is verified against the wire**, not assumed. With the gateway set
> to Hash 1-in-512 the header read `mode=2 interval=512` and the decoder scaled
> by 512; with sampling switched off it reads `mode=0 interval=0` and the
> decoder scales by 1. Both were confirmed by capturing packets and decoding
> the header by hand.
>
> **Flow totals are a fraction of the SNMP counters for the same interface, and
> that is by design.** Measured on the pilot with sampling off, flow accounted
> for 3.3% of SNMP bytes on a bridge interface and 10.5% on a tunnel. The cause
> is §1's top-N cut, confirmed rather than assumed: every interface sat at the
> ten-bucket cap in every one-minute interval, so everything outside the busiest
> ten conversations was discarded before storage. Two smaller contributors are
> the export covering only the networks selected on the device, and SNMP
> counting a transiting flow on both interfaces where flow attributes it to one.
>
> This is why ADR-020 makes SNMP the authority for volume and flow the answer to
> composition. The rule now has a measurement behind it.
>
> An earlier version of this document stated that UniFi gateways cannot export
> flow. **That was wrong** — the feature is in the UniFi Network application
> under Traffic Logging, offering v5, v9 and IPFIX. The claim was never tested,
> only inferred from the absence of an sFlow MIB on the switches. Recorded
> rather than quietly deleted, because a confident false claim is worse than a
> gap and this one would have stopped an operator from trying.
>
> v9, IPFIX, IPv6 flows and options-record sampling remain exercised only
> against a generated source. §9 lists what is still unwritten.

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
interval to top-N talkers, conversations and applications per interface — plus
the protocol, DSCP, AS, subnet and TCP-flag dimensions of §3, several of which
are bounded by what they are rather than by N — and writes only those. The cardinality is then roughly
`N × interfaces × 3`, which the existing stack handles without a new datastore.

**N is `NETINV_FLOW_TOPN`, default 25.** It began at 10, chosen against a
generated exporter, and real hardware showed that to be too tight: on a pilot
gateway every interface sat at the cap in every single interval, so the tables
were permanently clipped and the share column described the kept ten rather
than the link.

The right value depends on fleet size, which is why it is a setting rather than
a constant. Cost is `N × 3 dimensions × 2 metrics` series per interface per
exporter for the open-ended dimensions — 150 at the default. `subnet` adds up to
another 50, and `as` the same where the exporter fills it; `protocol`, `dscp`
and `tcp_flags` add a handful, because a network carries few of any of them. A
thousand flow-exporting devices with ten interfaces each would come to 2–2.7M
active series, past NFR-03's ~1M
single-node target, so a fleet that size must lower it.

The instantaneous count is not the whole cost, and the difference is large.
Every distinct talker creates a series and top-N membership churns, so series
*created* over time far exceeds series *active*: the pilot, three exporters at
N=10, held 319 active series while creating 9,513 distinct ones in 24 hours.
Raising N raises that churn roughly in proportion, and retention decides how
long the index carries it.

The cost is stated plainly because it is not recoverable: **a conversation
outside the top N for an interval was never written.** It did not go unqueried —
it does not exist. "Every flow from this host last Tuesday" is raw per-flow
retention, which needed ClickHouse and its own ADR — ADR-026 added it (§7)
beside the aggregates rather than in place of them.

## 2. What it decodes

| Format | Port | Status |
|---|---|---|
| NetFlow v5 | 2055/udp | **Implemented** |
| NetFlow v9 | 2055/udp | **Implemented**, including options templates and sampling (§2.1–2.2) |
| IPFIX | 4739/udp | **Implemented**, including variable-length and enterprise fields (§2.1) |
| sFlow v5 | 6343/udp | **Implemented** (ADR-026): flow samples and expanded flow samples, with raw packet headers and the switch, router and gateway extensions (§2.3) |

The collector listens on **2055, 4739 and 6343** and reads the format from the
datagram, so any of the four formats works on any of the ports. An exporter that
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

### 2.3 sFlow

sFlow is not a flow export. An sFlow agent samples one packet in N, copies the
first bytes of its header, and sends that sample with the interface it crossed
and the sampling rate. NetInv turns **each sample into one record**: addresses,
ports, protocol, TOS and TCP flags parsed from the copied Ethernet/802.1Q/IP
header (IPv4 and IPv6), bytes = frame length × rate, packets = rate. The
extended switch (VLANs), router (next hop, masks) and gateway (BGP next hop, AS
path) records fill what the header cannot. An sFlow record therefore has no
duration and no start or end time, and its TCP flags are one packet's, not the
OR across a flow.

The datagram is told apart from NetFlow by its first four bytes: sFlow's
version is a 32-bit 5, so its first two bytes read as 0, a version no NetFlow or
IPFIX packet carries. Counter samples are skipped — interface counters come from
SNMP. The bounds that matter on an unauthenticated port are in the decoder: at
most 512 samples per datagram, 64 records per sample and 256 AS-path entries,
and every length checked against what remains.

### 2.4 Fields kept for raw storage

The aggregates read only addresses, ports, protocol, TOS, AS, masks, TCP flags
and counters. Raw storage (§7) keeps everything below as well, each **zero
when the export did not carry it** — an exporter sends what its template
names, and most send a fraction of this.

| Field | NetFlow v9 / IPFIX element | Notes |
|---|---|---|
| Start / end time | 22/21 (uptime-relative), 150–153 (absolute), 160 (IPFIX boot time) | v5 and v9 convert uptime with the header's own uptime/Unix pair, safe across the 49.7-day wrap |
| Source / destination MAC | 56, 81 / 80, 57 | Stored as 48-bit integers |
| VLAN | 58, 59, 243 | Priority bits masked off |
| Next hop / BGP next hop | 15, 62 / 18, 63 | v5 carries the next hop too |
| Direction | 61 | Stored 1 ingress, 2 egress, 0 unknown |
| ICMP type/code | 32, 139 | type<<8 \| code |
| Application | 95 (id, shown `engine:selector`), 96 (name) | NBAR, App-ID, FortiOS application control |
| NAT translation | 225–228, 281, 282 | Post-NAT addresses and ports |
| Firewall event | 233, Cisco ASA 40005 | 1 created, 2 deleted, 3 denied, 4 alert, 5 update |
| User | 371, Cisco ASA 40000 | Cut at 128 bytes, kept to valid UTF-8 |
| Sampling rate | — | The 1-in-N the counters were scaled by; 1 when unsampled |

A record carrying its own inline sampling rate is scaled by it and **not** again
by the exporter's options-template rate; before ADR-026 both applied, which
would have reported N² times the traffic for an exporter that sent both.

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
| `dimension` | `talker`, `conversation`, `application`, `protocol`, `dscp`, `as`, `subnet` or `tcp_flags` |
| `value` | The talker/conversation/application itself |
| `sampled` | `"true"` only when the export declared sampling |

`exporter` is the **datagram's source address**, not any field inside it.
NetFlow v5 carries no exporter identity, and a field that a sender controls
would be a poor key for attributing traffic to a device in any case.

The dimensions:

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
- **`protocol`** — the IP protocol by name (`tcp`, `udp`, `gre`, `esp`,
  `icmp6`), otherwise `ip/N`. Answers "how much of this is tunnel overhead".
- **`dscp`** — the top six bits of the ToS byte, named by per-hop behaviour:
  `be`, `ef`, `af11`–`af43`, `cs1`–`cs7`, `voice-admit`, otherwise `dscp/N`.
  Whether traffic is *marked* the way the QoS policy expects is a question
  counters cannot answer at all. Read from v5's fixed ToS field and IE 5.
- **`as`** — `AS<n>`, counted at both ends as talkers are, but once when both
  ends share an AS. Read from v5's 16-bit fields and IE 16/17 (16- or 32-bit).
  **AS 0 is skipped**: it means the exporter did not know, and most edge
  gateways — UniFi included — never fill it, so on such a fleet this dimension
  is simply empty rather than showing everything as one network.
- **`subnet`** — the network each end sits in, as a prefix (`10.1.0.0/16`),
  counted at both ends but once when both share it. The length is the
  exporter's route to that end where it reports one (v5's mask bytes, IE 9/13,
  IPv6 IE 29/30), otherwise **a fixed /24 or /48**. A mask of 0 is replaced
  rather than believed: it means "not reported" or a default route, and either
  way would put the whole Internet in one row. The length is part of the value,
  so a fallback /24 cannot be mistaken for a route the exporter holds — but a
  fleet mixing exporters that report masks with ones that do not will show the
  same address space at two granularities.
- **`tcp_flags`** — the flags a TCP flow carried, OR'd across its packets,
  rendered `SYN,ACK,PSH,FIN` (opening, data, closing, then the rare bits);
  `none` where the exporter sent zero. Non-TCP flows are not counted. **`SYN`
  alone** is the row to watch: the initiator never acknowledged anything, so the
  connection never completed — a scan, or a service not answering. `RST` marks
  refused or torn-down connections. Each direction is its own flow record, so
  the reply side of a failed attempt, if any, appears separately. Read from v5's
  flags byte and IE 6 (IPFIX's 16-bit `tcpControlBits` contributes its low
  byte). At most 256 values by construction.

The protocol, DSCP and AS dimensions were added (2026-10-23) for the flow API
(doc 09 §7.1), and subnet and TCP flags the same day. They are aggregates like
the rest. The further fields of §2.4 go only to raw storage (§7), and a field
the collector does not read at all is skipped by its declared length.

### 3.1 Device attribution

Flow is keyed by the datagram's source address, and NetInv matches that against
a device's management address. Real routers routinely export from somewhere
else — an uplink or a loopback — so a device can carry a list of **additional
exporter addresses** (`flow_exporters` on the device, settable via `PATCH
/devices/{id}`). Both are matched, so claiming an address changes attribution
without touching how the device is polled.

Two pilot gateways demonstrated the need: managed on their LAN addresses, they
export from their WAN side, so their flow arrived, decoded, and belonged to no
device. The Flow tab's empty state names the unattributed addresses it can see
and offers to claim one, because that state is where the problem is discovered.

> **A MetricsQL trap, if you build a selector from that list.** Matching several
> addresses needs `exporter=~"a|b"`, and the escaping has two layers: MetricsQL
> unescapes the double-quoted string first, then compiles what remains as a
> regex. A single backslash never reaches the regex — it is consumed as a string
> escape, and `\.` is not a valid one, so the store rejects the entire query
> with a parse error. The query text needs `\\.` to yield `\.` at the regex.
> Use `reQuote` from `api/hooks.ts` rather than escaping by hand.

### 3.2 Interface attribution

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
| `NETINV_FLOW_ADDR` | `:2055,:4739,:6343` | Comma-separated UDP listen addresses |
| `NETINV_FLOW_ALLOW` | *(empty)* | Comma-separated CIDRs or bare addresses |
| `NETINV_FLOW_TOPN` | `25` | Buckets kept per interface per dimension per interval (§1) |
| `NETINV_VM_URL` | — | VictoriaMetrics write endpoint |
| `NETINV_FLOW_CH_URL` | *(empty: no raw storage)* | ClickHouse HTTP endpoint (§7); Compose sets `http://clickhouse:8123` |
| `NETINV_FLOW_CH_USER` / `_PASSWORD` | — | Compose uses `netinv` and `CH_PASSWORD` from `.env` |
| `NETINV_FLOW_RAW_RETENTION` | `30` | Days of raw flows (`30` or `30d`) |
| `NETINV_FLOW_ROLLUP_RETENTION` | `365` | Days of hourly rollups |
| `NETINV_FLOW_GEO_DIR` | `/var/lib/netinv-flow/geo` | Where the enrichment databases are read from (§7.4) |

The api reads the four `NETINV_FLOW_CH_*`/retention variables as well — it
serves the explorer and needs to know where raw data ends — so set them on
both, as the Compose overlay does.

A bare address in `NETINV_FLOW_ALLOW` becomes a `/32` or `/128`, which is what
an operator listing a single exporter means.

The Compose stack publishes `2055/udp`, `4739/udp` and `6343/udp` from the
`flow` service.
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

1. **Prefer NetFlow v9 or IPFIX.** v5, v9, IPFIX and sFlow are all decoded,
   but v9 and IPFIX carry the most (IPv6, timestamps, NAT, application, user —
   §2.4), while sFlow is sampled headers with no duration (§2.3) and v5 is
   IPv4-only.
2. **Set the active timeout to 1 minute.** Defaults are typically 30 minutes:
   a long-lived transfer is then reported once per half hour, as one enormous
   record, and the chart shows a spike surrounded by nothing rather than a
   sustained flow. One minute matches the aggregation interval (§3.2).
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
- **Sampling defaults to on, and turning it off is worth doing.** The pilot's
  gateway shipped at Hash 1-in-512. NetInv reads the rate from the header,
  scales by it and marks the series `sampled="true"` — verified correct against
  a packet capture — but at that rate on a quiet link very few flows are
  sampled at all, and the result is sparse and unreconcilable. Switching
  **Sampling Mode → Off** took the pilot from 7 minutes of data in 45 to 27 in
  30, and from 18 series to 166. Counted bytes also cost nothing here: this is
  a gateway, not a core router.
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
  buckets, which in practice means flows with no usable ifIndex (§3.2).
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

### 5.1 Alerting on staleness

Until 2026-08-17 nothing alerted when flow stopped, and the cost of that was
measured rather than imagined. On the pilot:

| exporter | last sample | noticed after |
|---|---|---|
| remote site A | 2026-08-15 16:57:55Z | 42 h |
| remote site B | 2026-08-15 16:58:55Z | 42 h |
| local gateway | 2026-08-17 09:50:55Z | 1.4 h |

The first two stopped within a minute of each other while the third kept going,
so every fleet-wide flow query stayed non-empty and the Flow tab kept drawing
charts. **Partial loss is the case that needs alerting**, and it is the one a
naive "is any flow arriving" check misses entirely.

Two built-in rules cover it (FR-ALR-07, migration 0012):

**A firing flow alert names a host, not just an address.** Flow series carry
only `exporter` and `if_index` — no `device`, `device_id` or `site`, unlike every
other metric family — so without help the alert most likely to reach someone at
3am is the one that says least, and the alert list can offer no graph link
because the instance has no device. The alerter resolves the exporter address
before firing: first against `devices.attrs->'flow_exporters'` (§3.1), then
falling back to the management address, which is what a gateway exports from
when the collector sits on its own LAN. `device` is set either way — to the
claiming device, or to the raw address when nothing claims it — so an unclaimed
exporter shows up as an address where a name should be, which is itself the
thing to fix. Resolution happens **after** fingerprinting, so claiming an
exporter onto a device while an alert is live does not resolve it and fire a
duplicate.

- **`ar_flow_exporter_stale`** (primary) —
  `count by (exporter) (last_over_time(netinv_flow_bytes[7d])) unless count by (exporter) (last_over_time(netinv_flow_bytes[20m]))`.
  One alert per stale exporter, labelled with `exporter`, which
  `absent_over_time` cannot provide — it reports the absence of a whole selector
  and returns no labels. Covers total loss too, since when everything stops
  everything is stale. **Self-gating**: an installation that never received flow
  has no exporters in the long window, so the rule yields nothing. That matters
  because flow is optional (ADR-020) and most deployments run without it.
- **`ar_flow_absent`** —
  `absent_over_time(netinv_flow_bytes[20m]) and on() count(last_over_time(netinv_flow_bytes[30d]))`.
  Extends coverage past the primary rule's 7 d window. **The gate is not
  optional**: bare `absent_over_time(netinv_flow_bytes[20m])` returns 1 when the
  metric has never existed, so ungated this rule fires on every deployment that
  never configured flow.

Two things deliberately avoided in both expressions:

1. **No `timestamp(last_over_time(...))`.** That form does not advance reliably
   under `-search.latencyOffset` plus result caching and reads as stale while
   collection is perfectly healthy. Presence via `last_over_time` does not have
   the problem.
2. **No unbounded scans.** Measured against the pilot's 32 588 flow series on a
   30 s evaluation cycle: 52 ms for the stale rule, 57 ms for the absent rule.

**The known limitation, stated plainly:** 7 d and 30 d are ceilings. An exporter
silent for longer leaves the window, its alert resolves, and the fleet goes quiet
about a fault that is still there — precisely the defect FR-ALR-08 rejects
lookback windows for. It is accepted here only because the principled fix needs a
metric NetInv does not publish yet: an inventory-derived
`netinv_flow_exporter_expected{exporter=…}` built from
`devices.attrs->'flow_exporters'` (§3.1), which would make the expected exporter
set a *stored fact* — the equivalent of `inventory.interfaces.ever_up` — instead
of a window, and would let both rules drop their long lookbacks entirely. That is
the follow-up; `netinv-flow` currently has no database access, so it cannot
publish it without new plumbing.

### 5.2 Judging whether flow stopped

`/api/v1/export` is the authority, and the only query form that cannot mislead
here:

```sh
curl -s --get http://localhost:8428/api/v1/export \
  --data-urlencode 'match[]=netinv_flow_bytes' \
  --data-urlencode "start=$(date -u -d '-2 hours' +%s)" --data-urlencode "end=$(date +%s)"
```

It returns raw stored samples with their timestamps — no fill, no lookbehind, no
caching. Three query forms that *do* mislead, all encountered while diagnosing
the outage above:

- **A coarse `step` fills forward.** A `query_range` at `step=1800` reported a
  populated bucket at 11:00 when the newest real sample was 09:50 — the lookbehind
  window is `max(step, 5m)`, so a 30-minute step carries a stale value forward for
  half an hour and the series looks alive.
- **Gap detection that ends at the last populated bucket is blind to a trailing
  gap.** Scanning 7 d of buckets reported *100 % coverage* while flow had been
  dead for 80 minutes, because the scan's own window ended at the last bucket
  containing data. Always compare the newest sample against `now` explicitly.
- **An instant query answers "no data" for a healthy series** whose newest sample
  is inside the 30 s latency offset.

And when the metric says nothing is arriving, confirm it at the wire before
blaming the collector — but capture for **longer than one active timeout**. The
UniFi default is 5 minutes (§4.2), so a 90-second `tcpdump` can fall between
bursts and read as silence. A useful positive control: send a garbage datagram at
the port and check the collector counts it —

```sh
printf 'PROBE' > /dev/udp/<netinv-host>/2055
# expect: flow intake … undecodable:1   (proves the receive path is live)
```

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

**Raw storage changes what a spoofed sender can do, not whether it can.** With
ClickHouse configured, every accepted flow becomes a row, so a sender passing
the allow-list can now fill disk rather than only bend the aggregates. The
writer's queue is bounded (§7.5) and the TTL bounds how long rows live, but
neither bounds the rate at which rows arrive — on an untrusted segment,
`NETINV_FLOW_ALLOW` and a firewall rule matter more than they did. ClickHouse
itself is not published outside the host: the Compose file binds 8123 to
localhost for development, and it has its own password (`CH_PASSWORD`, generated
by `quickstart.sh` and added by `upgrade.sh` to an older `.env`).

## 7. Raw flow storage (ADR-026)

### 7.1 What is stored

`netinv-flow` hands every decoded packet to a raw writer after the aggregator
(`flow.RawSink`; the aggregator goes first because it is the path that must not
miss). The writer turns each record into one row of `netinv_flow.flows`
(doc 08 §9): received time, start/end, exporter, format, sampling rate,
interfaces, addresses, ports, protocol, TOS, TCP flags, AS numbers and names,
countries, masks, MACs, VLANs, next hops, direction, ICMP type, application,
NAT, firewall event, user, bytes and packets. Rows are partitioned by day and
ordered by `(exporter, received)`, because nearly every question is scoped to a
device and a window.

**Time is the received time.** Exporters flush on their own timeouts — UniFi's
active timeout is 5 minutes (§4.2) — so a long flow's bytes land at the moment
the exporter reported them, as they do in the aggregates. Start and end are kept
for the individual-flow view, not used to spread bytes over time.

The collector owns the schema and applies it at every start
(`flowstore.EnsureSchema`), idempotently; a changed retention is applied with
`ALTER … MODIFY TTL` only when it differs. ClickHouse may start after the
collector, so the schema is retried every 10 s in the background while the
writer queues.

### 7.2 The hourly rollup

A materialized view sums every inserted block into `netinv_flow.flows_1h`
(SummingMergeTree), keyed by hour, exporter, interfaces, addresses, ports,
protocol, TOS, AS, country, VLAN, direction, application and format, with
bytes, packets and a flow count. It **drops what makes rows unique**: MAC, NAT,
user, TCP flags, next hop, and ephemeral ports — a port ≥ 32768 is stored as 0,
so a client's random source port does not make every connection its own row.

The explorer reads the rollup when the window starts before the raw data does,
or is longer than 7 days and uses only rollup fields. A query that needs a
raw-only field past the raw retention is refused with the field named, rather
than answered from a table that does not have it. Rollup rows for one key
arriving in different blocks are summed by merges later, so every query on it
`sum()`s — a rollup row is never read as final.

### 7.3 Sizing

Raw storage grows with **flows per second**, not with devices. Measured on the
generator with every field populated: **~46 bytes per flow** compressed in the
raw table and ~18 per rollup row. Real exporters fill fewer fields and repeat
more, so treat these as an upper bound:

| Flows/s | Per day | 30 days raw |
|---|---|---|
| 10 | 0.86 M flows, ~40 MB | ~1.2 GB |
| 100 | 8.6 M, ~400 MB | ~12 GB |
| 1 000 | 86 M, ~4 GB | ~120 GB |

The rollup is bounded by distinct host pairs × services per hour, which for
most networks is far below the raw count; with random traffic (the generator)
it is not, which is the case to plan for if the network is mostly scans.
Platform → Capacity does not include ClickHouse yet; the Flows page header shows
rows and bytes on disk from `system.parts`.

`deploy/clickhouse/netinv.xml` caps the server at 2 GB of memory and switches off
ClickHouse's own diagnostic logs (query, trace, metric, part logs), which
otherwise write continuously and grow without a TTL. It idles at ~150 MB.

### 7.4 Enrichment: country and AS name

With the databases present, every row gets a source and destination country,
AS numbers where the exporter left them zero, and AS names. The data is **not in
the repository**: `scripts/fetch-flow-geo.sh` downloads iptoasn's
`ip2asn-combined` (PDDL, public domain) and DB-IP's country lite (CC BY 4.0) into
`flow-geo/`, which the Compose overlay mounts read-only. The collector reloads
them within 5 minutes of the files changing, so the monthly refresh needs no
restart. Both together load in ~1.3 s into ~41 MB, and a lookup takes ~66 ns.

DB-IP's licence requires the visible credit "IP Geolocation by DB-IP"; the Flows
page shows it whenever country data is loaded. Enrichment happens at write time
and is not backfilled: rows written before the files existed stay without
country.

### 7.5 Operating it

- **The writer never blocks intake.** It queues up to 1 000 000 rows; past
  that, new flows are dropped from raw storage and counted, while the aggregates
  carry on. An insert that fails is retried with the same deduplication token,
  so a timeout after a successful write does not double-count — in the raw
  table or in the rollup, which carries its own deduplication window for that
  reason.
- **The collector reports itself** to `netinv_flow.collector_status` every
  minute: rows written, dropped, failed inserts, queue depth, and how many
  enrichment ranges are loaded. The Flows page and `GET /flow/explore/status`
  read it. `flow raw queue full` in the log is the drop warning.
- **Raw flows are not backed up** by `scripts/backup.sh` (ADR-026). A restored
  deployment has 2 years of aggregates and no raw flows until new ones arrive.
- **Turning it off** is unsetting `NETINV_FLOW_CH_URL` on `flow` and `api`; the
  page then explains how to turn it on. The `clickhouse` service still runs
  unless removed from the stack.

## 8. The UI

### 8.1 The Flows page

`/flows` (doc 30 §5c) is the explorer over §7. Its whole state is in the URL, so
a view can be bookmarked or pasted; the device Flow tab links to it with the
device already filtered.

- **Group by** up to four fields, with the top 5–100 groups by bits/s,
  packets/s or flows/s. Interfaces are keyed as `exporter|ifIndex` — an
  ifIndex means nothing across devices — and named from inventory.
- **Filters** on any field, `is` / `is not`, several values ORed; addresses take
  CIDR prefixes. "Either end" fields (address, port, AS, country, VLAN) match
  source or destination. **Clicking any value** in a table or the Sankey adds
  it as a filter, which is how an investigation narrows.
- **A time-series chart** of the top groups and the total; a **Sankey** when
  grouping by exactly two fields; a **table** with share bars and CSV export;
  and **individual flows**, newest or largest first, with columns the exporter
  never fills left out.
- The header states which store answered (raw or hourly rollup), how many flows
  are stored and on how much disk, and any drops.

### 8.2 The device Flow tab

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

## 9. What this increment does not include

Stated explicitly so nobody reads §2's table as a to-do list that is nearly
finished:

- **sFlow counter samples are ignored**, and sFlow from real hardware has not
  been seen; the decoder is tested against generated datagrams and fuzzed.
- **Raw storage is not in Platform → Capacity**, not backed up, and has no
  alert of its own for a full queue — the log line and the Flows page header
  are what show it.
- **No IPv6 exporter transport.** Flow *about* IPv6 traffic works (v9 carries
  it); the collector still listens on UDP over IPv4 only.
- **Template state is not persisted across a restart.** It could be, and that
  would remove the 10-20 minute gap after a restart, but it means writing
  attacker-influenced state to disk — see ADR-022 before deciding that is worth
  it.
- **The Flow tab and dashboard panel still read the aggregates** through the
  metrics proxy. The Flows page (§8.1) is where the protocol, DSCP, AS and
  every other field are.
- **Only one real exporter, on one platform, in one format.** A UniFi gateway
  exporting v5 is now validated end to end (§4.2). v9, IPFIX, IPv6 and
  options-record sampling have been exercised only against a generated source,
  and no Cisco, Juniper, Huawei, FortiGate or PAN-OS exporter has ever reached
  this collector. Reports from those remain the most useful thing an outside
  contributor could send — see `/CONTRIBUTING.md` and the hardware-validation
  ask in Discussions.
