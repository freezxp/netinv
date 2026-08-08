// Shared React Flow canvas pieces: device node, definition<->flow mapping.
import { Handle, Position, type Edge, type Node as RFNode } from "@xyflow/react";
import type { LiveData, MapDefinition, MapLink, MapNode } from "./api";
import { utilColor } from "./api";
import { formatBps } from "../../lib/format";
import { cx } from "../../components/ui";

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

// One handle per side, each declared source *and* target (paired with
// ConnectionMode.Loose in the editor) so a link can be dragged from any side of
// any node to any side of another. A single top-source/bottom-target pair made
// linking two side-by-side nodes almost impossible.
export const handleSides = [
  { id: "t", position: Position.Top },
  { id: "r", position: Position.Right },
  { id: "b", position: Position.Bottom },
  { id: "l", position: Position.Left },
] as const;

export function DeviceNode({
  data,
  isConnectable,
}: {
  data: DeviceNodeData;
  isConnectable?: boolean;
}) {
  // Nodes with no device behind them never take a live state, so a solid
  // status border would be a lie — a caption is not "unknown", it is simply
  // not monitored. Dashed and muted says that without a legend.
  const plain = data.kind !== "device";
  const caption = data.kind === "label";

  return (
    <div
      className={cx(
        "px-3 py-1.5 text-xs font-medium",
        caption
          ? "text-slate-600 dark:text-slate-300"
          : "rounded-md border-2 bg-white shadow-sm dark:bg-slate-900 dark:text-slate-200",
        plain && !caption && "border-dashed",
      )}
      style={
        caption
          ? undefined
          : {
              borderColor: plain
                ? "var(--status-muted)"
                : (nodeStateColor[data.state] ?? "var(--status-muted)"),
            }
      }
    >
      {/* Handles stay mounted in the read-only viewer — edges anchor to them —
          but are invisible there, so a published map shows no editing dots. */}
      {handleSides.map((h) => (
        <Handle
          key={h.id}
          id={h.id}
          type="source"
          position={h.position}
          className="!h-2 !w-2 !border-0 !bg-slate-400"
          style={{ opacity: isConnectable ? 1 : 0 }}
        />
      ))}
      {data.kind === "site" ? "▣ " : data.kind === "cloud" ? "☁ " : ""}
      {data.label}
    </div>
  );
}

export const nodeTypes = { netinv: DeviceNode };

export interface CactiEdgeData extends Record<string, unknown> {
  /** Toward the target node (out of the A endpoint). */
  utilOut: number;
  outBps: number;
  /** Toward the source node (into the A endpoint). */
  utilIn: number;
  inBps: number;
  state: string;
  /** False when no capacity is known, so percentages would be meaningless. */
  hasCapacity: boolean;
  bound: boolean;
  /** Shown instead of rates when there is no live data — i.e. in the editor. */
  label?: string;
}

// Cacti's weathermap signature: one line split at its midpoint, each half
// coloured by the traffic flowing *toward* the node it touches, with an
// arrowhead pointing into that node. Reading it needs no legend lookup — the
// busy direction is the one that changed colour.
export function CactiEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  data,
}: {
  id: string;
  sourceX: number;
  sourceY: number;
  targetX: number;
  targetY: number;
  data?: CactiEdgeData;
}) {
  const d = data;
  const mx = (sourceX + targetX) / 2;
  const my = (sourceY + targetY) / 2;

  if (!d || !d.bound) {
    return (
      <g>
        <path
          className="react-flow__edge-interaction"
          d={`M${sourceX},${sourceY} L${targetX},${targetY}`}
          stroke="transparent"
          strokeWidth={18}
          fill="none"
          style={{ pointerEvents: "stroke" }}
        />
        <path
          className="react-flow__edge-path"
          d={`M${sourceX},${sourceY} L${targetX},${targetY}`}
          stroke="#64748b"
          strokeWidth={1.5}
          strokeDasharray="4 3"
          fill="none"
        />
        {d?.label && (
          <text
            x={mx}
            y={my - 6}
            textAnchor="middle"
            style={{ fontSize: 10, fill: "#94a3b8", pointerEvents: "none" }}
          >
            {d.label}
          </text>
        )}
      </g>
    );
  }

  // Half touching the source carries what arrives there; half touching the
  // target carries what leaves the A endpoint.
  const inColor = utilColor(d.utilIn, d.state);
  const outColor = utilColor(d.utilOut, d.state);

  // Arrowheads sit a little inside the endpoints so they don't disappear
  // beneath the node boxes.
  const angle = Math.atan2(targetY - sourceY, targetX - sourceX);
  const arrow = (x: number, y: number, dir: number, color: string) => {
    const size = 7;
    const a = angle + dir;
    const p1 = [x, y];
    const p2 = [x - size * Math.cos(a - 0.4), y - size * Math.sin(a - 0.4)];
    const p3 = [x - size * Math.cos(a + 0.4), y - size * Math.sin(a + 0.4)];
    return (
      <polygon
        points={`${p1[0]},${p1[1]} ${p2[0]},${p2[1]} ${p3[0]},${p3[1]}`}
        fill={color}
      />
    );
  };
  const inset = 14;
  const sxi = sourceX + inset * Math.cos(angle);
  const syi = sourceY + inset * Math.sin(angle);
  const txi = targetX - inset * Math.cos(angle);
  const tyi = targetY - inset * Math.sin(angle);

  const pct = (v: number) => (d.hasCapacity ? ` ${v.toFixed(0)}%` : "");

  return (
    <g className="netinv-cacti-edge">
      {/* Fat invisible path first: the hover target, so a 5px line is still
          easy to hit with a mouse. pointerEvents is forced back on because the
          viewer runs with elementsSelectable off, which otherwise makes edges
          ignore the mouse entirely. */}
      <path
        className="react-flow__edge-interaction"
        d={`M${sourceX},${sourceY} L${targetX},${targetY}`}
        stroke="transparent"
        strokeWidth={18}
        fill="none"
        style={{ pointerEvents: "stroke" }}
      />
      <path
        d={`M${sxi},${syi} L${mx},${my}`}
        stroke={inColor}
        strokeWidth={5}
        strokeLinecap="butt"
        fill="none"
      />
      <path
        d={`M${mx},${my} L${txi},${tyi}`}
        stroke={outColor}
        strokeWidth={5}
        strokeLinecap="butt"
        fill="none"
      />
      {arrow(sxi, syi, Math.PI, inColor)}
      {arrow(txi, tyi, 0, outColor)}
      <text
        x={(sxi + mx) / 2}
        y={(syi + my) / 2 - 7}
        textAnchor="middle"
        style={{ fontSize: 9, fill: "#94a3b8", pointerEvents: "none" }}
      >
        {formatBps(d.inBps)}
        {pct(d.utilIn)}
      </text>
      <text
        x={(mx + txi) / 2}
        y={(my + tyi) / 2 - 7}
        textAnchor="middle"
        style={{ fontSize: 9, fill: "#94a3b8", pointerEvents: "none" }}
      >
        {formatBps(d.outBps)}
        {pct(d.utilOut)}
      </text>
      <title>{`${formatBps(d.inBps)} in / ${formatBps(d.outBps)} out${
        d.hasCapacity ? "" : " — no capacity set, so no utilisation colour"
      }`}</title>
      <desc>{id}</desc>
    </g>
  );
}

export const edgeTypes = { cacti: CactiEdge };

export function toFlow(
  def: MapDefinition,
  live?: LiveData,
): { nodes: RFNode<DeviceNodeData>[]; edges: Edge[] } {
  // Every array here is guarded: `live?.x` only guards `live`, and a map with
  // no links yet is the case that reaches the viewer as null.
  const liveNodes = new Map((live?.nodes ?? []).map((n) => [n.id, n.state]));
  const liveLinks = new Map((live?.links ?? []).map((l) => [l.id, l]));
  const nodes = (def.nodes ?? []).map((n) => ({
    id: n.id,
    type: "netinv" as const,
    position: { x: n.x, y: n.y },
    data: {
      label: n.label || n.text || n.device_id || n.id,
      kind: n.kind,
      state: liveNodes.get(n.id) ?? "unknown",
    },
  }));
  const edges = (def.links ?? []).map((l) => {
    const lv = liveLinks.get(l.id);
    return {
      id: l.id,
      source: l.from,
      target: l.to,
      sourceHandle: l.from_handle ?? null,
      targetHandle: l.to_handle ?? null,
      type: "cacti" as const,
      data: {
        utilIn: lv?.util_in ?? 0,
        utilOut: lv?.util_out ?? 0,
        inBps: lv?.in_bps ?? 0,
        outBps: lv?.out_bps ?? 0,
        state: lv?.state ?? "nodata",
        // Distinguishes an idle link from one with no capacity to measure
        // against — both read as 0%, but only one is worth colouring.
        hasCapacity: (lv?.capacity_bps ?? 0) > 0,
        bound: !!lv && !!l.a_endpoint,
        label: lv
          ? undefined
          : l.a_endpoint
            ? `if ${l.a_endpoint.if_index}`
            : "unbound",
      } satisfies CactiEdgeData,
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
