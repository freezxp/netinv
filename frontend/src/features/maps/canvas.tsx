// Shared React Flow canvas pieces: device node, definition<->flow mapping.
import {
  Handle,
  Position,
  type Edge,
  type Node as RFNode,
} from "@xyflow/react";
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
  // A site or cloud node usually stands for something real that simply is not
  // pollable — a mesh AP with no SNMP agent, an ISP — so it is drawn exactly
  // like a device node. Only its border colour differs, staying muted because
  // there is no live state to report; dashing it as well made a real part of
  // the topology look provisional. A label is the exception: it is a caption,
  // not a thing, so it gets no box at all.
  const caption = data.kind === "label";
  const plain = data.kind !== "device";

  return (
    <div
      className={cx(
        "px-3 py-1.5 text-xs font-medium",
        caption
          ? "text-slate-600 dark:text-slate-300"
          : "rounded-md border-2 bg-white shadow-sm dark:bg-slate-900 dark:text-slate-200",
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
  /** Node names, so the tooltip can say which way each figure runs. */
  sourceLabel?: string;
  targetLabel?: string;
}

// One band per link, running in whichever direction carries more traffic.
//
// This began as Cacti's split line — each half coloured by the traffic flowing
// toward the node it touched. That is faithful to Cacti but busy: every link
// carried two colours, two arrowheads and two numbers, so a map of thirteen
// links presented fifty-two things to read, and the eye had to compare halves
// before learning anything.
//
// A link's interesting property is usually its heavier direction, so that is
// what the band shows. The quieter direction stays in the tooltip rather than
// on the canvas. Comparing bytes per second is equivalent to comparing
// utilisation here, because a link has one capacity shared by both directions.
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

  // "out" runs from the A endpoint toward the target; "in" arrives back at the
  // source. Ties fall to out so a fully idle link still draws one consistent
  // way rather than flickering between them on rounding noise.
  const forward = d.outBps >= d.inBps;
  const busyBps = forward ? d.outBps : d.inBps;
  const busyUtil = forward ? d.utilOut : d.utilIn;
  const quietBps = forward ? d.inBps : d.outBps;
  const quietUtil = forward ? d.utilIn : d.utilOut;
  const color = utilColor(busyUtil, d.state);

  const angle = Math.atan2(targetY - sourceY, targetX - sourceX);
  const inset = 14;
  const sxi = sourceX + inset * Math.cos(angle);
  const syi = sourceY + inset * Math.sin(angle);
  const txi = targetX - inset * Math.cos(angle);
  const tyi = targetY - inset * Math.sin(angle);

  // An arrowhead sitting on the node edge competes with the node box and gets
  // lost. Placed partway along its own half it has clear space around it, and
  // it is the thing the eye lands on first.
  const arrowAt = (
    fromX: number,
    fromY: number,
    toX: number,
    toY: number,
    color: string,
  ) => {
    const t = 0.55; // just past the midpoint of the half, in open space
    const x = fromX + (toX - fromX) * t;
    const y = fromY + (toY - fromY) * t;
    const a = Math.atan2(toY - fromY, toX - fromX);
    const size = 11;
    const spread = 0.45;
    return (
      <polygon
        points={[
          `${x + size * 0.5 * Math.cos(a)},${y + size * 0.5 * Math.sin(a)}`,
          `${x - size * Math.cos(a - spread)},${y - size * Math.sin(a - spread)}`,
          `${x - size * Math.cos(a + spread)},${y - size * Math.sin(a + spread)}`,
        ].join(" ")}
        fill={color}
        stroke="var(--map-arrow-edge, #0f172a)"
        strokeWidth={0.75}
      />
    );
  };

  const pct = (v: number) => (d.hasCapacity ? ` · ${v.toFixed(0)}%` : "");
  // The path is drawn in the direction of travel, so the dash animation moves
  // the way the traffic does without needing a reversed set of keyframes.
  const fromX = forward ? sxi : txi;
  const fromY = forward ? syi : tyi;
  const toX = forward ? txi : sxi;
  const toY = forward ? tyi : syi;
  const flowPath = `M${fromX},${fromY} L${toX},${toY}`;

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
      <path d={flowPath} stroke={color} strokeWidth={5} fill="none" />
      {/* Darker dashes crawling along the band, in the direction of travel. */}
      {busyBps > 0 && (
        <path
          className="netinv-flow"
          d={flowPath}
          stroke="rgba(15,23,42,0.45)"
          strokeWidth={5}
          strokeDasharray="5 19"
          fill="none"
        />
      )}
      {arrowAt(fromX, fromY, toX, toY, color)}
      {/* Deliberately not at the midpoint. Two links that cross usually cross
          near their middles — the SD-WAN mesh has exactly that pair — and
          midpoint labels then sit on top of each other. A third of the way
          along, in the direction of travel, keeps them apart and reads as
          where the flow starts. */}
      <EdgeRate
        x={fromX + (toX - fromX) * 0.32}
        y={fromY + (toY - fromY) * 0.32}
        angle={angle}
        text={`${formatBps(busyBps)}${pct(busyUtil)}`}
      />
      <title>{`${formatBps(busyBps)} toward ${
        (forward ? d.targetLabel : d.sourceLabel) ?? "the far end"
      }${pct(busyUtil)} — the heavier direction, which is what the band shows. Reverse: ${formatBps(
        quietBps,
      )} toward ${(forward ? d.sourceLabel : d.targetLabel) ?? "the other end"}${pct(
        quietUtil,
      )}${d.hasCapacity ? "" : " — no capacity set, so no utilisation colour"}`}</title>
      <desc>{id}</desc>
    </g>
  );
}

// Rate text offset clear of the band it belongs to, always on the same side of
// the line so the two directions never swap places as a map is rearranged.
function EdgeRate({
  x,
  y,
  angle,
  text,
}: {
  x: number;
  y: number;
  angle: number;
  text: string;
}) {
  // Perpendicular to the line. On a steep link that perpendicular is nearly
  // horizontal, and a centred label straddles the band — its halo then paints
  // over the arrowhead, which is the one thing that has to stay readable. So
  // steep links get the text anchored fully to one side instead.
  const px = Math.sin(angle);
  const py = -Math.cos(angle);
  const steep = Math.abs(px) > 0.5;
  const off = steep ? 11 : 13;
  return (
    <text
      x={x + px * off}
      y={y + py * off}
      textAnchor={steep ? (px > 0 ? "start" : "end") : "middle"}
      dominantBaseline="middle"
      style={{
        fontSize: 10,
        fill: "currentColor",
        paintOrder: "stroke",
        stroke: "var(--map-label-halo, #020617)",
        strokeWidth: 3,
        strokeLinejoin: "round",
        pointerEvents: "none",
      }}
      className="text-slate-500 dark:text-slate-300"
    >
      {text}
    </text>
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
  const nodeLabels = new Map(
    (def.nodes ?? []).map((n) => [n.id, n.label || n.text || n.id]),
  );
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
        // Either end counts as bound now, so a B-only link must not be
        // labelled "unbound" — it reads correctly, it was just drawn the
        // other way round.
        label: lv
          ? undefined
          : (l.a_endpoint ?? l.b_endpoint)
            ? `if ${(l.a_endpoint ?? l.b_endpoint)!.if_index}`
            : "unbound",
        sourceLabel: nodeLabels.get(l.from),
        targetLabel: nodeLabels.get(l.to),
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
