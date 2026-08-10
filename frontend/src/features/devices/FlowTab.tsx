import { useMemo, useState } from "react";

import {
  flowRateExpr,
  flowSelector,
  flowTotalExpr,
  flowWindow,
  useDeviceInterfaces,
  toFlowRows,
  useInstantQuery,
  useQueryRange,
  type FlowDimension,
} from "../../api/hooks";
import { useTimeRange } from "../../api/timerange";
import { FlowTable } from "../../components/FlowTable";
import { FlowSetupGuide } from "./FlowSetupGuide";
import { TimeSeries } from "../../components/TimeSeries";
import { Card, cx, Select } from "../../components/ui";
import { formatBps } from "../../lib/format";

const DIMENSIONS: Array<{ key: FlowDimension; label: string; blurb: string }> =
  [
    {
      key: "talker",
      label: "Talkers",
      blurb: "Individual hosts, counted in whichever direction they appeared.",
    },
    {
      key: "conversation",
      label: "Conversations",
      blurb:
        "Host pairs. Both directions of an exchange are counted together, so A→B and B→A share one row.",
    },
    {
      key: "application",
      label: "Applications",
      blurb:
        "The well-known port of the exchange, which is as close to “what application” as flow gets without inspecting payloads.",
    },
  ];

// How many series the chart draws. The table shows everything the collector
// kept; the chart shows only the head of it, because ten overlapping lines
// answer no question a reader actually has.
const CHART_SERIES = 5;

export function FlowTab({
  deviceID,
  mgmtIP,
}: {
  deviceID: string;
  mgmtIP: string;
}) {
  const range = useTimeRange();
  const [dimension, setDimension] = useState<FlowDimension>("talker");
  const [ifIndex, setIfIndex] = useState("");

  const interfaces = useDeviceInterfaces(deviceID);

  // Three probes, because "no chart" has three different causes and an
  // operator needs to know which one they have (doc 34 §5). Nothing arriving
  // anywhere, nothing arriving from this device, and a device that exports
  // from an address other than the one NetInv manages it on are three separate
  // problems with three separate fixes.
  const anyFlow = useInstantQuery("count(netinv_flow_bytes)");
  const exporters = useInstantQuery("count by (exporter) (netinv_flow_bytes)");
  const deviceIfs = useInstantQuery(
    `count by (if_index) (netinv_flow_bytes{exporter="${mgmtIP}"})`,
    !!mgmtIP,
  );

  const selector = flowSelector(mgmtIP, dimension, ifIndex || undefined);
  const rangeS = Math.round(range.hours * 3600);
  const windowS = flowWindow(range.stepS);

  const totals = useInstantQuery(flowTotalExpr(selector, rangeS), !!mgmtIP);
  const series = useQueryRange(
    flowRateExpr(selector, windowS),
    range.hours,
    range.stepS,
  );
  const sampled = useInstantQuery(
    `count(netinv_flow_bytes{exporter="${mgmtIP}",sampled="true"})`,
    !!mgmtIP,
  );
  const isSampled = (sampled.data?.length ?? 0) > 0;

  const rows = useMemo(() => toFlowRows(totals.data ?? []), [totals.data]);

  // Chart only the heaviest few, chosen by the same ranking as the table so
  // the two agree.
  const head = new Set(rows.slice(0, CHART_SERIES).map((r) => r.value));
  const chart = (series.data ?? []).filter((s) => head.has(s.metric.value));

  const ifOptions = useMemo(() => {
    const withFlow = (deviceIfs.data ?? [])
      .map((r) => r.metric.if_index)
      .filter(Boolean);
    return withFlow.map((idx) => {
      const iface = interfaces.data?.data.find(
        (i) => String(i.if_index) === idx,
      );
      return {
        idx,
        label: iface ? `${iface.name} (${idx})` : `ifIndex ${idx}`,
      };
    });
  }, [deviceIfs.data, interfaces.data]);

  const loading = anyFlow.isLoading || deviceIfs.isLoading;
  const haveDeviceFlow = (deviceIfs.data?.length ?? 0) > 0;

  if (!loading && !haveDeviceFlow) {
    return (
      <NoFlow
        mgmtIP={mgmtIP}
        anywhere={(anyFlow.data?.length ?? 0) > 0}
        exporters={(exporters.data ?? [])
          .map((r) => r.metric.exporter)
          .filter(Boolean)}
      />
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex rounded-md border border-slate-200 dark:border-slate-800">
          {DIMENSIONS.map((d) => (
            <button
              key={d.key}
              onClick={() => setDimension(d.key)}
              className={cx(
                "px-3 py-1.5 text-sm first:rounded-l-md last:rounded-r-md",
                dimension === d.key
                  ? "bg-sky-600 text-white"
                  : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
              )}
            >
              {d.label}
            </button>
          ))}
        </div>
        <Select
          value={ifIndex}
          onChange={(e) => setIfIndex(e.target.value)}
          className="w-56"
        >
          <option value="">All interfaces</option>
          {ifOptions.map((o) => (
            <option key={o.idx} value={o.idx}>
              {o.label}
            </option>
          ))}
        </Select>
        {isSampled && (
          <span
            className="rounded bg-amber-100 px-2 py-0.5 text-xs text-amber-800 dark:bg-amber-950 dark:text-amber-300"
            title="The exporter is sampling, so these are extrapolations. The interface counters on the Interfaces tab remain the authority for how much traffic crossed the link."
          >
            sampled — estimated
          </span>
        )}
      </div>

      <p className="text-xs text-slate-500 dark:text-slate-400">
        {DIMENSIONS.find((d) => d.key === dimension)?.blurb}
      </p>

      <Card
        title={`Top ${DIMENSIONS.find((d) => d.key === dimension)?.label.toLowerCase()} (${range.short})`}
      >
        <FlowTable
          rows={rows}
          dimension={dimension}
          rangeShort={range.short}
          rangeS={rangeS}
        />
      </Card>

      <Card title={`Top ${CHART_SERIES} over time (${range.short})`}>
        <TimeSeries
          result={chart}
          windowHours={range.hours}
          format={formatBps}
          label={(m) => m.value ?? "—"}
        />
      </Card>

      <p className="text-xs text-slate-500 dark:text-slate-400">
        Only the busiest few buckets per interface are kept each minute
        (ADR-020). Anything outside that cut was never recorded, so these totals
        describe the top of the traffic, not all of it.
      </p>

      {/* Collapsed once flow is working — still reachable, because setting up
          the second exporter is the normal next step after the first. */}
      <Card>
        <details>
          <summary className="cursor-pointer text-sm font-medium text-slate-600 dark:text-slate-300">
            How to configure NetFlow v5 on a device
          </summary>
          <div className="mt-4">
            <FlowSetupGuide destIP={collectorHost()} />
          </div>
        </details>
      </Card>
    </div>
  );
}

// The address to give the exporter. The browser reached NetInv on it, so it is
// the best available guess at an address the device can also reach — but only a
// guess: the collector may run on another host, or sit behind NAT. The guide
// says so rather than presenting it as fact.
//
// A loopback address is the one answer that is never right and is silently
// wrong: pasted into a router, "localhost" points the exporter at itself, and
// the result is a correct-looking configuration that sends flow nowhere. An
// obvious placeholder is better than a plausible mistake.
const LOOPBACK = new Set(["localhost", "127.0.0.1", "::1", "[::1]"]);

export function collectorHost() {
  const h = window.location.hostname;
  if (!h || LOOPBACK.has(h.toLowerCase())) return "";
  return h;
}

// The empty state carries the diagnosis rather than a shrug. "No data" on a
// feature that depends on device-side configuration is the least useful thing
// a page can say, and the failure this collector actually presents is silence
// (doc 34 §5).
function NoFlow({
  mgmtIP,
  anywhere,
  exporters,
}: {
  mgmtIP: string;
  anywhere: boolean;
  exporters: string[];
}) {
  const others = exporters.filter((e) => e !== mgmtIP);
  return (
    <Card>
      <div className="mx-auto max-w-xl py-6 text-sm text-slate-600 dark:text-slate-300">
        {!anywhere ? (
          <>
            <div className="mb-2 font-semibold">
              No flow data is reaching NetInv.
            </div>
            <p className="mb-3">
              Nothing has been received from any device. Point an exporter at
              this host on <span className="mono">UDP 2055</span> to populate
              this tab.
            </p>
          </>
        ) : (
          <>
            <div className="mb-2 font-semibold">
              Flow is arriving, but none of it from{" "}
              <span className="mono">{mgmtIP}</span>.
            </div>
            <p className="mb-3">
              Flow is attributed by the <em>source address of the datagram</em>,
              which is not always the address NetInv manages the device on — a
              router exporting from a loopback is the usual reason for this
              mismatch.
              {others.length > 0 && (
                <>
                  {" "}
                  Currently receiving from:{" "}
                  <span className="mono">{others.join(", ")}</span>.
                </>
              )}
            </p>
          </>
        )}
        <div className="mt-5 border-t border-slate-200 pt-4 dark:border-slate-800">
          <FlowSetupGuide destIP={collectorHost()} />
        </div>
      </div>
    </Card>
  );
}
