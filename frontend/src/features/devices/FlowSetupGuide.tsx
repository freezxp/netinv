// In-product guide for configuring a device to export NetFlow v5.
//
// This lives in the UI rather than only in doc 34 because of where someone
// stands when they need it: looking at an empty Flow tab, deciding whether the
// feature is broken or simply unconfigured. A pointer to a file in the source
// repository asks them to leave, find the repo, and trust that it is current.
//
// The destination address is substituted into every snippet, because the one
// value the operator must get right is the one a generic guide cannot supply.
import { useState } from "react";

import { Button, cx } from "../../components/ui";

interface Vendor {
  key: string;
  label: string;
  /** `%DEST%` is replaced with the collector address. */
  config: string;
  note?: string;
}

// Deliberately not exhaustive. These cover the platforms NetInv has connectors
// for, plus the two escape hatches — a software exporter for anything whose
// firmware cannot do it, and RouterOS/VyOS for the small routers that show up
// at branch sites.
const VENDORS: Vendor[] = [
  {
    key: "cisco",
    label: "Cisco IOS / IOS-XE",
    config: `ip flow-export version 5
ip flow-export destination %DEST_IP% %DEST_PORT%
ip flow-cache timeout active 1
ip flow-cache timeout inactive 15
!
interface GigabitEthernet0/1
 ip flow ingress`,
    note: "On older IOS the per-interface command is `ip route-cache flow` instead of `ip flow ingress`.",
  },
  {
    key: "junos",
    label: "Juniper Junos",
    config: `set forwarding-options sampling instance NETINV family inet output flow-server %DEST_IP% port %DEST_PORT%
set forwarding-options sampling instance NETINV family inet output flow-server %DEST_IP% version 5
set forwarding-options sampling instance NETINV input rate 100
set interfaces ge-0/0/0 unit 0 family inet sampling input`,
    note: "Junos samples rather than caching every flow, so figures arrive as estimates. NetInv scales them by the declared rate and marks the tab “sampled”.",
  },
  {
    key: "huawei",
    label: "Huawei VRP (NetStream)",
    config: `ip netstream export version 5
ip netstream export host %DEST_IP% %DEST_PORT%
ip netstream timeout active 1
#
interface GigabitEthernet0/0/1
 ip netstream inbound`,
  },
  {
    key: "routeros",
    label: "MikroTik RouterOS",
    config: `/ip traffic-flow set enabled=yes active-flow-timeout=1m
/ip traffic-flow target add dst-address=%DEST_IP% port=%DEST_PORT% version=5`,
  },
  {
    key: "vyos",
    label: "VyOS",
    config: `set system flow-accounting netflow version 5
set system flow-accounting netflow server %DEST_IP% port %DEST_PORT%
set system flow-accounting netflow timeout expiry-interval 60
set system flow-accounting interface eth0`,
  },
  {
    key: "softflowd",
    label: "Linux / pfSense / OPNsense",
    config: `softflowd -i eth0 -v 5 -t maxlife=60 -n %DEST_IP%:%DEST_PORT%`,
    note: "pfSense and OPNsense both package softflowd. This is also how to get flow off a device whose own firmware cannot export it — run it on a host that sees the traffic.",
  },
];

// Stated before the snippets because each one produces silence rather than an
// error, and silence is indistinguishable from "no exporter configured".
const RULES: Array<{ head: string; body: string }> = [
  {
    head: "Set version 5 explicitly",
    body: "Nearly every current platform defaults to v9 or IPFIX. NetInv does not decode those yet, so an exporter sending them looks exactly like one sending nothing. This is the most common reason a correctly-pointed exporter shows up empty.",
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
    head: "NetFlow v5 is IPv4-only",
    body: "The v5 record format has no IPv6 fields at all, so a dual-stack link silently reports only half its traffic. If your traffic is substantially IPv6, v5 is the wrong format and NetInv cannot read the right one yet.",
  },
];

const PLACEHOLDER = "<netinv-host>";

export function FlowSetupGuide({
  destIP,
  destPort = 2055,
}: {
  /** Empty when the address cannot be guessed; a placeholder is shown instead. */
  destIP: string;
  destPort?: number;
}) {
  const dest = destIP || PLACEHOLDER;
  const guessed = !!destIP;
  const [vendor, setVendor] = useState(VENDORS[0].key);
  const [copied, setCopied] = useState(false);
  const current = VENDORS.find((v) => v.key === vendor) ?? VENDORS[0];
  const config = current.config
    .replaceAll("%DEST_IP%", dest)
    .replaceAll("%DEST_PORT%", String(destPort));

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
      <div className="mb-3">
        Send NetFlow v5 to{" "}
        <span className="mono rounded bg-slate-100 px-1.5 py-0.5 dark:bg-slate-800">
          {dest}:{destPort}
        </span>{" "}
        over UDP.
        <span className="text-slate-500 dark:text-slate-400">
          {" "}
          {guessed
            ? "That is this NetInv host as your browser reached it; if the collector runs elsewhere, or the device reaches it through NAT, use the address the device can actually get to."
            : "You reached NetInv over loopback, so the browser cannot tell what address a device would use — substitute the one this host is reachable on from the network."}
        </span>
      </div>

      <ol className="mb-4 space-y-1.5">
        {RULES.map((r, i) => (
          <li key={r.head} className="flex gap-2">
            <span className="text-slate-400 tabular-nums">{i + 1}.</span>
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
        {VENDORS.map((v) => (
          <button
            key={v.key}
            onClick={() => setVendor(v.key)}
            className={cx(
              "rounded px-2 py-1 text-xs",
              v.key === vendor
                ? "bg-sky-600 text-white"
                : "bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700",
            )}
          >
            {v.label}
          </button>
        ))}
      </div>

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
            2. <span className="mono">tcpdump -ni any udp port {destPort}</span>{" "}
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
          </li>
        </ol>
      </div>
    </div>
  );
}

// The snippets are duplicated in doc 34 §4.2 for readers who never open the
// app. If you change one, change both — the doc is the contributor-facing copy
// and this is the operator-facing one.
