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
    </div>
  );
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
        <ul className="list-disc space-y-1 pl-5 text-slate-500 dark:text-slate-400">
          <li>
            <strong>NetFlow v5 only, and it must be set explicitly.</strong>{" "}
            Most platforms default to v9 or IPFIX, which are not decoded yet —
            an exporter sending those looks exactly like one sending nothing.
            This is the most common reason a correctly-pointed exporter shows up
            here empty.
          </li>
          <li>
            <strong>Set the active timeout to 1 minute.</strong> The usual
            30-minute default reports a long transfer once per half hour as one
            huge record, which charts as a spike surrounded by nothing.
          </li>
          <li>
            Enable export <em>on the interfaces</em>, ingress direction — global
            configuration alone collects nothing on most platforms. A flow with
            no ingress or egress ifIndex cannot be attributed and is discarded.
          </li>
          <li>
            The collector logs <span className="mono">flow intake</span> when it
            receives packets it cannot use, so its log distinguishes a
            misconfigured exporter from no exporter at all.
          </li>
          <li>
            Per-vendor configuration snippets and a step-by-step check are in{" "}
            <span className="mono">docs/34-flow-collection.md</span> §4.2–4.3.
          </li>
        </ul>
      </div>
    </Card>
  );
}
