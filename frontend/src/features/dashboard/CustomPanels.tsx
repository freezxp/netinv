// The two panel kinds that carry their own configuration: an embedded
// weathermap, and a chart of any metric the deployment publishes.
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { ReactFlow, Background, ConnectionMode } from "@xyflow/react";
import { api } from "../../api/client";
import { useQueryRange, useMetricsLimits } from "../../api/hooks";
import { rateWindow, useTimeRange } from "../../api/timerange";
import { Card, EmptyState } from "../../components/ui";
import { TimeSeries } from "../../components/TimeSeries";
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
