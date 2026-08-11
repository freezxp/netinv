// In-product guide for configuring a device to export flow.
//
// This lives in the UI rather than only in doc 34 because of where someone
// stands when they need it: looking at an empty Flow tab, deciding whether the
// feature is broken or simply unconfigured. A pointer to a file in the source
// repository asks them to leave, find the repo, and trust that it is current.
//
// The destination address and port are substituted into every snippet, because
// those are the values a generic guide cannot supply and the operator must get
// right.
import { useState } from "react";

import { Button, cx } from "../../components/ui";

/** Export formats NetInv decodes. sFlow is not one of them. */
type Version = "v9" | "ipfix" | "v5";

const VERSIONS: Array<{
  key: Version;
  label: string;
  port: number;
  blurb: string;
}> = [
  {
    key: "v9",
    label: "NetFlow v9",
    port: 2055,
    blurb:
      "The usual choice, and what most platforms default to. Carries IPv6, unlike v5. Records are described by templates the exporter sends periodically, so flow is briefly missing after a NetInv restart until those arrive.",
  },
  {
    key: "ipfix",
    label: "IPFIX",
    port: 4739,
    blurb:
      "The IETF standardisation of v9, and equivalent here — same template model, same fields, same IPv6 support. Choose it if your platform's IPFIX support is the better maintained of the two; as far as NetInv is concerned there is no advantage either way.",
  },
  {
    key: "v5",
    label: "NetFlow v5",
    port: 2055,
    blurb:
      "The oldest and simplest: fixed records, no templates, so nothing is missing after a restart. It is IPv4-only — on a dual-stack link it silently reports about half the traffic — and carries no VLAN or MPLS context. Prefer v9 unless the device offers nothing else.",
  },
];

interface Vendor {
  key: string;
  label: string;
  /**
   * Per-version configuration. A missing entry means the platform cannot
   * export that version, which is said out loud rather than left blank: an
   * operator hunting for a setting that does not exist wastes more time than
   * one told plainly it is not there.
   */
  config: Partial<Record<Version, string>>;
  /** Click-path instructions, for platforms with no useful CLI form. */
  gui?: Partial<Record<Version, string>>;
  note?: string;
}

// Not exhaustive: the platforms NetInv has connectors for, plus the two escape
// hatches — a software exporter for anything whose firmware cannot do it, and
// RouterOS/VyOS for the small routers that turn up at branch sites.
const VENDORS: Vendor[] = [
  {
    key: "cisco",
    label: "Cisco IOS / IOS-XE",
    config: {
      v9: `ip flow-export version 9
ip flow-export destination %DEST_IP% %DEST_PORT%
ip flow-export template refresh-rate 30
ip flow-cache timeout active 1
ip flow-cache timeout inactive 15
!
interface GigabitEthernet0/1
 ip flow ingress`,
      v5: `ip flow-export version 5
ip flow-export destination %DEST_IP% %DEST_PORT%
ip flow-cache timeout active 1
ip flow-cache timeout inactive 15
!
interface GigabitEthernet0/1
 ip flow ingress`,
    },
    note: "On older IOS the per-interface command is `ip route-cache flow` rather than `ip flow ingress`. IPFIX on IOS-XE means Flexible NetFlow with a custom flow record and exporter — a different and longer configuration than the one shown here.",
  },
  {
    key: "junos",
    label: "Juniper Junos",
    config: {
      v9: `set services flow-monitoring version9 template NETINV-V4 ipv4-template
set services flow-monitoring version9 template NETINV-V4 template-refresh-rate seconds 60
set forwarding-options sampling instance NETINV input rate 100
set forwarding-options sampling instance NETINV family inet output flow-server %DEST_IP% port %DEST_PORT%
set forwarding-options sampling instance NETINV family inet output flow-server %DEST_IP% version9 template NETINV-V4
set interfaces ge-0/0/0 unit 0 family inet sampling input`,
      ipfix: `set services flow-monitoring version-ipfix template NETINV-V4 ipv4-template
set services flow-monitoring version-ipfix template NETINV-V4 template-refresh-rate seconds 60
set forwarding-options sampling instance NETINV input rate 100
set forwarding-options sampling instance NETINV family inet output flow-server %DEST_IP% port %DEST_PORT%
set forwarding-options sampling instance NETINV family inet output flow-server %DEST_IP% version-ipfix template NETINV-V4
set interfaces ge-0/0/0 unit 0 family inet sampling input`,
      v5: `set forwarding-options sampling instance NETINV input rate 100
set forwarding-options sampling instance NETINV family inet output flow-server %DEST_IP% port %DEST_PORT%
set forwarding-options sampling instance NETINV family inet output flow-server %DEST_IP% version 5
set interfaces ge-0/0/0 unit 0 family inet sampling input`,
    },
    note: "Junos samples rather than caching every flow, so its figures are estimates. NetInv scales them by the declared rate and marks the tab “sampled”. For IPv6, add the equivalent family inet6 stanza and an ipv6-template.",
  },
  {
    key: "huawei",
    label: "Huawei VRP (NetStream)",
    config: {
      v9: `ip netstream export version 9
ip netstream export host %DEST_IP% %DEST_PORT%
ip netstream timeout active 1
ip netstream export template timeout-rate 1
#
interface GigabitEthernet0/0/1
 ip netstream inbound`,
      v5: `ip netstream export version 5
ip netstream export host %DEST_IP% %DEST_PORT%
ip netstream timeout active 1
#
interface GigabitEthernet0/0/1
 ip netstream inbound`,
    },
  },
  {
    key: "fortigate",
    label: "FortiGate",
    config: {
      v9: `config system netflow
    set collector-ip %DEST_IP%
    set collector-port %DEST_PORT%
    set active-flow-timeout 60
    set template-tx-timeout 60
end
config system interface
    edit "port1"
        set netflow-sampler both
    next
end`,
      ipfix: `config system netflow
    set collector-ip %DEST_IP%
    set collector-port %DEST_PORT%
    set active-flow-timeout 60
end
config system interface
    edit "port1"
        set netflow-sampler both
    next
end`,
    },
    note: "FortiOS exports v9 by default and offers no v5 at any version. The per-interface netflow-sampler is the step that is easy to miss — the global block alone configures a collector and exports nothing. On builds that expose it, template-tx-timeout controls how quickly templates are resent after a NetInv restart.",
  },
  {
    key: "panos",
    label: "Palo Alto PAN-OS",
    config: {},
    gui: {
      v9: "Device → Server Profiles → NetFlow: add a server at %DEST_IP%:%DEST_PORT% and set the template refresh short — PAN-OS defaults to 30 minutes, which is also how long flow stays missing after a NetInv restart. Then assign that profile to each ingress interface under Network → Interfaces → interface → Advanced → NetFlow Profile. Assigning it to the interface is the equivalent of FortiOS's per-interface sampler, and the same thing to forget.",
    },
    note: "PAN-OS exports NetFlow v9 only — no v5, no IPFIX.",
  },
  {
    key: "routeros",
    label: "MikroTik RouterOS",
    config: {
      v9: `/ip traffic-flow set enabled=yes active-flow-timeout=1m
/ip traffic-flow target add dst-address=%DEST_IP% port=%DEST_PORT% version=9`,
      ipfix: `/ip traffic-flow set enabled=yes active-flow-timeout=1m
/ip traffic-flow target add dst-address=%DEST_IP% port=%DEST_PORT% version=ipfix`,
      v5: `/ip traffic-flow set enabled=yes active-flow-timeout=1m
/ip traffic-flow target add dst-address=%DEST_IP% port=%DEST_PORT% version=5`,
    },
  },
  {
    key: "vyos",
    label: "VyOS",
    config: {
      v9: `set system flow-accounting netflow version 9
set system flow-accounting netflow server %DEST_IP% port %DEST_PORT%
set system flow-accounting netflow timeout expiry-interval 60
set system flow-accounting interface eth0`,
      ipfix: `set system flow-accounting netflow version 10
set system flow-accounting netflow server %DEST_IP% port %DEST_PORT%
set system flow-accounting netflow timeout expiry-interval 60
set system flow-accounting interface eth0`,
      v5: `set system flow-accounting netflow version 5
set system flow-accounting netflow server %DEST_IP% port %DEST_PORT%
set system flow-accounting netflow timeout expiry-interval 60
set system flow-accounting interface eth0`,
    },
    note: "VyOS spells IPFIX “version 10”, which is the protocol's own version number.",
  },
  {
    key: "softflowd",
    label: "Linux / pfSense / OPNsense",
    config: {
      v9: `softflowd -i eth0 -v 9 -t maxlife=60 -n %DEST_IP%:%DEST_PORT%`,
      ipfix: `softflowd -i eth0 -v 10 -t maxlife=60 -n %DEST_IP%:%DEST_PORT%`,
      v5: `softflowd -i eth0 -v 5 -t maxlife=60 -n %DEST_IP%:%DEST_PORT%`,
    },
    note: "pfSense and OPNsense both package softflowd. This is also how to get flow off a device whose own firmware cannot export it — run it on a host that sees the traffic.",
  },
];

// Stated before the snippets because each produces silence rather than an
// error, and silence is indistinguishable from "no exporter configured".
const RULES: Array<{ head: string; body: string; only?: Version[] }> = [
  {
    head: "Send NetFlow or IPFIX — not sFlow",
    body: "NetFlow v5, v9 and IPFIX are all decoded. sFlow is not, and an exporter sending it looks exactly like one sending nothing.",
  },
  {
    head: "Bring the active timeout down to 1 minute",
    body: "Defaults are typically 30 minutes. A long-lived transfer is then reported once per half hour as a single enormous record, which charts as a spike surrounded by nothing rather than as sustained traffic.",
  },
  {
    head: "Enable it on the interfaces, ingress direction",
    body: "Global export configuration alone collects nothing on most platforms. A flow carrying no ingress or egress ifIndex cannot be attributed to an interface and is discarded.",
  },
  {
    head: "Set the template refresh short",
    body: "v9 and IPFIX records are meaningless without the template describing them, and NetInv forgets every template when it restarts. Until each exporter resends — 30 minutes on some defaults — its flow is missing. That gap is the protocol working, not a fault; the collector reports it as awaiting_template.",
    only: ["v9", "ipfix"],
  },
  {
    head: "This format is IPv4-only",
    body: "The v5 record has no IPv6 fields at all, so a v5 exporter on a dual-stack link silently reports about half its traffic. v9 and IPFIX both carry IPv6.",
    only: ["v5"],
  },
];

const PLACEHOLDER = "<netinv-host>";

export function FlowSetupGuide({
  destIP,
}: {
  /** Empty when the address cannot be guessed; a placeholder is shown instead. */
  destIP: string;
}) {
  const [version, setVersion] = useState<Version>("v9");
  const [vendor, setVendor] = useState(VENDORS[0].key);
  const [copied, setCopied] = useState(false);

  const dest = destIP || PLACEHOLDER;
  const guessed = !!destIP;
  const ver = VERSIONS.find((v) => v.key === version) ?? VERSIONS[0];
  const current = VENDORS.find((v) => v.key === vendor) ?? VENDORS[0];

  const fill = (t: string) =>
    t.replaceAll("%DEST_IP%", dest).replaceAll("%DEST_PORT%", String(ver.port));

  const snippet = current.config[version];
  const gui = current.gui?.[version];
  const config = snippet ? fill(snippet) : "";
  const rules = RULES.filter((r) => !r.only || r.only.includes(version));

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(config);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard access is denied over plain HTTP in most browsers, which is
      // a normal way to reach an on-prem tool. Failing silently would look
      // like a broken button, so say what happened.
      setCopied(false);
      alert(
        "The browser blocked clipboard access — select the text and copy it manually.",
      );
    }
  };

  return (
    <div className="text-sm text-slate-600 dark:text-slate-300">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <span className="text-xs text-slate-500 dark:text-slate-400">
          Format
        </span>
        <div className="flex rounded-md border border-slate-200 dark:border-slate-800">
          {VERSIONS.map((v) => (
            <button
              key={v.key}
              onClick={() => setVersion(v.key)}
              className={cx(
                "px-2.5 py-1 text-xs first:rounded-l-md last:rounded-r-md",
                v.key === version
                  ? "bg-sky-600 text-white"
                  : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
              )}
            >
              {v.label}
            </button>
          ))}
        </div>
      </div>
      <p className="mb-3 text-xs text-slate-500 dark:text-slate-400">
        {ver.blurb}
      </p>

      <div className="mb-3">
        Send {ver.label} to{" "}
        <span className="mono rounded bg-slate-100 px-1.5 py-0.5 dark:bg-slate-800">
          {dest}:{ver.port}
        </span>{" "}
        over UDP.
        <span className="text-slate-500 dark:text-slate-400">
          {" "}
          {guessed
            ? "That is this NetInv host as your browser reached it; if the collector runs elsewhere, or the device reaches it through NAT, use the address the device can actually get to."
            : "You reached NetInv over loopback, so the browser cannot tell what address a device would use — substitute the one this host is reachable on from the network."}{" "}
          NetInv listens on both 2055 and 4739 and reads the format from the
          datagram, so either port works for any of these.
        </span>
      </div>

      <ol className="mb-4 space-y-1.5">
        {rules.map((r, i) => (
          <li key={r.head} className="flex gap-2">
            <span className="tabular-nums text-slate-400">{i + 1}.</span>
            <span>
              <strong>{r.head}.</strong>{" "}
              <span className="text-slate-500 dark:text-slate-400">
                {r.body}
              </span>
            </span>
          </li>
        ))}
      </ol>

      <div className="mb-2 flex flex-wrap gap-1">
        {VENDORS.map((v) => {
          const supported = !!v.config[version] || !!v.gui?.[version];
          return (
            <button
              key={v.key}
              onClick={() => setVendor(v.key)}
              title={
                supported
                  ? undefined
                  : `${v.label} does not export ${ver.label}`
              }
              className={cx(
                "rounded px-2 py-1 text-xs",
                v.key === vendor
                  ? "bg-sky-600 text-white"
                  : supported
                    ? "bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700"
                    : // Dimmed, not hidden. A vendor vanishing from the list as
                      // the format changes reads as a rendering bug, and hiding
                      // it also hides the useful fact that it cannot do this.
                      "bg-slate-50 text-slate-400 hover:bg-slate-100 dark:bg-slate-900 dark:text-slate-600",
              )}
            >
              {v.label}
            </button>
          );
        })}
      </div>

      {gui ? (
        <div className="rounded-md border border-slate-200 bg-slate-50 p-3 text-xs leading-relaxed dark:border-slate-800 dark:bg-slate-900">
          {fill(gui)}
        </div>
      ) : snippet ? (
        <div className="relative">
          <pre className="mono overflow-x-auto rounded-md bg-slate-100 p-3 text-xs leading-relaxed dark:bg-slate-950">
            {config}
          </pre>
          <Button
            variant="ghost"
            className="absolute top-1.5 right-1.5 text-xs"
            onClick={copy}
          >
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
      ) : (
        <div className="rounded-md border border-amber-300 bg-amber-50 p-3 text-xs text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
          <strong>
            {current.label} does not export {ver.label}.
          </strong>{" "}
          Pick another format above — or, if it exports none that NetInv reads,
          run a software exporter on a host in the traffic path (see Linux /
          pfSense / OPNsense).
        </div>
      )}
      {current.note && (
        <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">
          {current.note}
        </p>
      )}

      <p className="mt-3 text-xs text-slate-500 dark:text-slate-400">
        <strong>Ubiquiti UniFi gateways cannot export flow</strong> — the
        firmware offers no NetFlow export and the switches do not advertise the
        sFlow MIB. Run a software exporter on a host behind them instead.
        Support on ZTE varies by model and software train, so no snippet is
        given rather than a wrong one.
      </p>

      <div className="mt-4 border-t border-slate-200 pt-3 dark:border-slate-800">
        <div className="mb-1 font-medium">Then check it, in this order</div>
        <ol className="space-y-1 text-xs text-slate-500 dark:text-slate-400">
          <li>
            1. <span className="mono">docker logs netinv-flow-1</span> — a{" "}
            <span className="mono">flow intake</span> line means packets are
            arriving but cannot be used. <strong>No output at all</strong> means
            nothing is arriving.
          </li>
          <li>
            2. <span className="mono">tcpdump -ni any udp port {ver.port}</span>{" "}
            on the NetInv host, if the log is silent — this separates “the
            network is not delivering it” from “the collector is not reading
            it”.
          </li>
          <li>
            3. Reload this tab. If it reports flow from an address that is not
            this device’s management IP, the problem is attribution, not
            collection.
          </li>
          <li>
            4. Allow up to two minutes: the collector aggregates for a minute
            before writing, and the metrics store withholds the most recent 30
            seconds from instant queries.
            {version !== "v5" && (
              <>
                {" "}
                On {ver.label}, allow one template refresh interval on top —
                until the exporter resends its templates its records cannot be
                read, and the log says{" "}
                <span className="mono">awaiting_template</span>.
              </>
            )}
          </li>
        </ol>
      </div>
    </div>
  );
}

// The snippets are duplicated in doc 34 §4.2 for readers who never open the
// app. If you change one, change both — the doc is the contributor-facing copy
// and this is the operator-facing one.
