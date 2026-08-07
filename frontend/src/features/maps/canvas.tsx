// Shared React Flow canvas pieces: device node, definition<->flow mapping.
import { Handle, Position, type Edge, type Node as RFNode } from "@xyflow/react";
import type { LiveData, MapDefinition, MapLink, MapNode } from "./api";
import { utilColor } from "./api";
import { formatBps } from "../../lib/format";

const nodeStateColor: Record<string, string> = {
  up: "var(--status-ok)",
  warning: "var(--status-warning)",
  critical: "var(--status-critical)",
  unreachable: "var(--status-unreachable)",
  unknown: "var(--status-muted)",
};

export interface DeviceNodeData extends Record<string, unknown> {
  label: string;
  kind: string;
  state: string;
}

export function DeviceNode({ data }: { data: DeviceNodeData }) {
  return (
    <div
      className="rounded-md border-2 bg-white px-3 py-1.5 text-xs font-medium shadow-sm dark:bg-slate-900 dark:text-slate-200"
      style={{ borderColor: nodeStateColor[data.state] ?? "var(--status-muted)" }}
    >
      <Handle type="source" position={Position.Top} className="!bg-slate-400" />
      {data.kind === "site" ? "▣ " : data.kind === "cloud" ? "☁ " : ""}
      {data.label}
      <Handle type="target" position={Position.Bottom} className="!bg-slate-400" />
    </div>
  );
}

export const nodeTypes = { netinv: DeviceNode };

export function toFlow(
  def: MapDefinition,
  live?: LiveData,
): { nodes: RFNode<DeviceNodeData>[]; edges: Edge[] } {
  const liveNodes = new Map(live?.nodes.map((n) => [n.id, n.state]));
  const liveLinks = new Map(live?.links.map((l) => [l.id, l]));
  const nodes = def.nodes.map((n) => ({
    id: n.id,
    type: "netinv" as const,
    position: { x: n.x, y: n.y },
    data: {
      label: n.label || n.text || n.device_id || n.id,
      kind: n.kind,
      state: liveNodes.get(n.id) ?? "unknown",
    },
  }));
  const edges = def.links.map((l) => {
    const lv = liveLinks.get(l.id);
    const util = lv ? Math.max(lv.util_in, lv.util_out) : 0;
    const color = lv ? utilColor(util, lv.state) : "#64748b";
    return {
      id: l.id,
      source: l.from,
      target: l.to,
      label: lv
        ? `${formatBps(lv.in_bps)} / ${formatBps(lv.out_bps)}`
        : l.a_endpoint
          ? `if ${l.a_endpoint.if_index}`
          : "unbound",
      style: { stroke: color, strokeWidth: lv ? 3 : 1.5 },
      labelStyle: { fontSize: 10, fill: "#94a3b8" },
      labelBgStyle: { fill: "transparent" },
      animated: lv ? util > 70 : false,
    } satisfies Edge;
  });
  return { nodes, edges };
}

// fromFlow folds RF positions back into the definition (positions are the
// only thing RF owns; bindings/kind live in the definition).
export function applyPositions(
  def: MapDefinition,
  nodes: RFNode<DeviceNodeData>[],
): MapDefinition {
  const pos = new Map(nodes.map((n) => [n.id, n.position]));
  return {
    ...def,
    nodes: def.nodes.map((n): MapNode => {
      const p = pos.get(n.id);
      return p ? { ...n, x: Math.round(p.x), y: Math.round(p.y) } : n;
    }),
  };
}

export function linkById(def: MapDefinition, id: string): MapLink | undefined {
  return def.links.find((l) => l.id === id);
}
