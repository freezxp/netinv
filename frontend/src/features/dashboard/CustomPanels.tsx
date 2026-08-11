// The two panel kinds that carry their own configuration: an embedded
// weathermap, and a chart of any metric the deployment publishes.
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { ReactFlow, Background, ConnectionMode } from "@xyflow/react";
import { api } from "../../api/client";
import {
  flowSelector,
  flowTotalExpr,
  toFlowRows,
  useInstantQuery,
  useMetricsLimits,
  useQueryRange,
} from "../../api/hooks";
import { rateWindow, useTimeRange } from "../../api/timerange";
import { Card, EmptyState } from "../../components/ui";
import { TimeSeries } from "../../components/TimeSeries";
import { DIMENSION_NOUN, FlowTable } from "../../components/FlowTable";
import { edgeTypes, nodeTypes, toFlow } from "../maps/canvas";
import { useMapDef, useMapLive } from "../maps/api";
import type { Panel } from "./layout";

export interface MapSummary {
  id: string;
  name: string;
}

export function useMaps() {
  return useQuery({
    queryKey: ["maps"],
    queryFn: () => api<{ data: MapSummary[] }>("/maps"),
    staleTime: 60_000,
  });
}

// Weathermap on the dashboard, read-only. Pan and zoom are disabled: this is a
// status view someone glances at, and a map that drifts when a cursor crosses
// it is worse than useless on a wall display.
export function WeathermapPanel({ panel }: { panel: Panel }) {
  const def = useMapDef(panel.mapID ?? "", "published");
  const live = useMapLive(panel.mapID ?? "", !!def.data);
  const maps = useMaps();
  const name =
    maps.data?.data.find((m) => m.id === panel.mapID)?.name ?? "Weathermap";

  if (!panel.mapID) {
    return (
      <Card title="Weathermap">
        <EmptyState>No map chosen — pick one in Customise.</EmptyState>
      </Card>
    );
  }
  if (def.isLoading) return <Card title={name}>Loading…</Card>;
  if (def.error || !def.data) {
    return (
      <Card title={name}>
        <EmptyState>
          This map could not be loaded. It may have been deleted, or never
          published — only published maps render here.
        </EmptyState>
      </Card>
    );
  }

  const flow = toFlow(def.data.definition, live.data);
  return (
    <Card title={panel.title || name}>
      <div className="h-[320px] w-full">
        <ReactFlow
          nodes={flow.nodes}
          edges={flow.edges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          // Must match the editor and viewer: nodes declare every handle as a
          // source, and Strict mode cannot resolve a target handle, so links
          // silently vanish.
          connectionMode={ConnectionMode.Loose}
          fitView
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable={false}
          panOnDrag={false}
          zoomOnScroll={false}
          zoomOnPinch={false}
          preventScrolling={false}
          proOptions={{ hideAttribution: true }}
        >
          <Background gap={16} />
        </ReactFlow>
      </div>
      <div className="mt-1 text-right">
        <Link
          to={`/maps/${panel.mapID}`}
          className="text-xs text-sky-600 hover:underline dark:text-sky-400"
        >
          Open full map →
        </Link>
      </div>
    </Card>
  );
}

export function useMetricNames() {
  return useQuery({
    queryKey: ["metrics", "names"],
    queryFn: () => api<{ data: string[] }>("/metrics/names"),
    staleTime: 5 * 60_000,
  });
}

// Chart of any published metric, with an optional label filter and grouping.
//
// The expression is assembled here rather than typed by the operator: a raw
// MetricsQL box is powerful and unusable, and this covers the cases a
// dashboard actually needs — one series per device, or a sum per site.
export function metricExpr(panel: Panel, pollS: number, stepS: number): string {
  if (!panel.metric) return "";
  const sel = panel.filter?.trim() ? `{${panel.filter.trim()}}` : "";
  let expr = `${panel.metric}${sel}`;
  if (panel.rate) expr = `rate(${expr}[${rateWindow(stepS, pollS)}])`;
  if (panel.groupBy?.trim()) {
    expr = `sum by (${panel.groupBy.trim()}) (${expr})`;
  }
  return expr;
}

export function MetricPanel({ panel }: { panel: Panel }) {
  const range = useTimeRange();
  const pollS = useMetricsLimits().data?.poll_interval_s ?? 0;
  const expr = metricExpr(panel, pollS, range.stepS);
  const q = useQueryRange(expr || `vector(0)`, range.hours, range.stepS);

  const title = panel.title || panel.metric || "Custom metric";
  if (!panel.metric) {
    return (
      <Card title="Custom metric">
        <EmptyState>No metric chosen — pick one in Customise.</EmptyState>
      </Card>
    );
  }
  return (
    <Card title={`${title} (${range.short})`}>
      {q.error ? (
        <EmptyState>
          {/* The proxy rejects a bad expression with the store's own parse
              error, which names the problem far better than "invalid query". */}
          {(q.error as Error).message}
        </EmptyState>
      ) : (
        <TimeSeries
          result={q.data ?? []}
          windowHours={range.hours}
          label={(m) =>
            m.device_label ||
            m.device ||
            m.site ||
            m.instance ||
            Object.values(m)[0] ||
            panel.metric ||
            "series"
          }
        />
      )}
    </Card>
  );
}

// Exporters seen recently, for the panel editor's picker. Offering a free-text
// box instead would invite a typo that renders an empty panel with no hint
// that the address is simply wrong.
//
// Over a day rather than the instant lookback: exporters send in bursts, and a
// picker built on the last few minutes would drop a perfectly healthy exporter
// out of the list between sends — while the operator was choosing it.
export function useFlowExporters() {
  const q = useInstantQuery(
    "count by (exporter) (max_over_time(netinv_flow_bytes[24h]))",
  );
  return (q.data ?? [])
    .map((r) => r.metric.exporter)
    .filter(Boolean)
    .sort();
}

const FLOW_DEFAULT_TOPN = 8;

// Top talkers, conversations or applications — fleet-wide by default, or for
// one exporter.
//
// Deliberately a table and not a chart. The dashboard question is "what is
// consuming the network right now", which is a ranking; five overlapping lines
// answer it far worse in the same space, and the device Flow tab already
// carries the time dimension for anyone who wants it.
export function FlowPanel({ panel }: { panel: Panel }) {
  const range = useTimeRange();
  const dimension = panel.flowDimension ?? "talker";
  const rangeS = Math.round(range.hours * 3600);
  const selector = flowSelector(panel.exporter ?? "", dimension);
  const totals = useInstantQuery(flowTotalExpr(selector, rangeS));
  const rows = toFlowRows(totals.data ?? []);

  const scope = panel.exporter ? panel.exporter : "all exporters";
  const title =
    panel.title ||
    `Top ${DIMENSION_NOUN[dimension].toLowerCase()}s — ${scope} (${range.short})`;

  return (
    <Card title={title}>
      {rows.length === 0 ? (
        <EmptyState>
          No flow recorded in this window. Flow arrives only from devices
          configured to export it; see the device’s Flow tab for what is
          reaching NetInv.
        </EmptyState>
      ) : (
        <FlowTable
          rows={rows}
          dimension={dimension}
          rangeShort={range.short}
          rangeS={rangeS}
          limit={panel.topN ?? FLOW_DEFAULT_TOPN}
        />
      )}
    </Card>
  );
}
