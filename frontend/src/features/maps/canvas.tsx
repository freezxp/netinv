// Shared React Flow canvas pieces: device node, definition<->flow mapping.
//
// The library's stylesheet is imported here, with the code that needs it, so
// that any chunk rendering a flow carries it. It used to be imported by the map
// pages only, which worked by accident while the app was a single bundle: the
// dashboard's weathermap panel was relying on a stylesheet pulled in by a page
// it never loads. Splitting the bundle turned that into a blank panel — nodes
// present in the DOM, `position: static`, stacked in document order.
import "@xyflow/react/dist/style.css";
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
  /**
   * Position within a bundle of links sharing the same endpoints and handles,
   * and how many there are. Coincident links are shifted perpendicular to the
   * line so each is visible and separately clickable; a lone link keeps the
   * exact centre line it always had.
   */
  lane?: number;
  lanes?: number;
}

// shortName trims a node label to something that fits beside a rate. "YY
// Gateway" becomes "YY", "Root AP" becomes "Root" — the first word carries the
// identity on every naming scheme this has met, and the tooltip still has the
// full name for anything ambiguous.
function shortName(label: string | undefined, fallback: string): string {
  const l = (label ?? "").trim();
  if (!l) return fallback;
  const first = l.split(/\s+/)[0];
  const pick = first.length >= 2 ? first : l;
  return pick.length > 10 ? pick.slice(0, 9) + "…" : pick;
}

// One band per link, running in whichever direction carries more traffic.
//
// This began as Cacti's split line — each half coloured by the traffic flowing
// toward the node it touched. That is faithful to Cacti but busy: every link
// carried two colours, two arrowheads and two numbers, so a map of thirteen
// links presented fifty-two things to read, and the eye had to compare halves
// before learning anything.
//
// A link's interesting property is usually its heavier direction, so the band
// takes its colour and its dash animation from that one. Both directions still
// get an arrowhead and a rate, because a speed without a direction is half an
// answer — but the reverse pair is drawn smaller and dimmer, so the map reads
// at a glance and rewards a closer look rather than demanding one.
//
// Comparing bytes per second is equivalent to comparing utilisation here,
// because a link has one capacity shared by both directions.
// Where each direction's arrow and its label sit along the line. Far enough
// apart that the two pairs never merge, and neither is at the midpoint, where
// two crossing links would stack their labels.
const BUSY_AT = 0.34;
const QUIET_AT = 0.7;

// Gap between parallel links. Wide enough that two 5px bands plus their labels
// read as separate lines rather than a thick one.
const LANE_SPACING = 16;

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
  // Shift coincident links off the shared centre line. The offset is
  // perpendicular to the line, so it works at any angle, and lanes are laid
  // out symmetrically about the centre: one link is unmoved, two straddle it,
  // three put the middle one back on the original line.
  const lanes = d?.lanes ?? 1;
  const lane = d?.lane ?? 0;
  const spread = lanes > 1 ? (lane - (lanes - 1) / 2) * LANE_SPACING : 0;
  const len = Math.hypot(targetX - sourceX, targetY - sourceY) || 1;
  const nx = (-(targetY - sourceY) / len) * spread;
  const ny = ((targetX - sourceX) / len) * spread;
  sourceX += nx;
  sourceY += ny;
  targetX += nx;
  targetY += ny;

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
  const sourceName = shortName(d.sourceLabel, "A");
  const targetName = shortName(d.targetLabel, "B");
  const busyTo = forward ? targetName : sourceName;
  const quietTo = forward ? sourceName : targetName;

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
    t = 0.55,
    size = 11,
    opacity = 1,
  ) => {
    const x = fromX + (toX - fromX) * t;
    const y = fromY + (toY - fromY) * t;
    const a = Math.atan2(toY - fromY, toX - fromX);
    const spread = 0.45;
    return (
      <polygon
        points={[
          `${x + size * 0.5 * Math.cos(a)},${y + size * 0.5 * Math.sin(a)}`,
          `${x - size * Math.cos(a - spread)},${y - size * Math.sin(a - spread)}`,
          `${x - size * Math.cos(a + spread)},${y - size * Math.sin(a + spread)}`,
        ].join(" ")}
        fill={color}
        fillOpacity={opacity}
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
      {/* Each rate sits beside the arrowhead it describes, and names where it
          is going. Position alone was not enough to pair them: the bright
          number sat at 30% of the line while its arrow was at 58%, so nothing
          tied the two together and the reader had to guess. The destination in
          the text settles it regardless of geometry — which matters most
          exactly where the map is hardest to read, on crossing and steep
          links. */}
      {arrowAt(fromX, fromY, toX, toY, color, BUSY_AT, 11, 1)}
      {arrowAt(toX, toY, fromX, fromY, color, 1 - QUIET_AT, 7, 0.55)}
      <EdgeRate
        x={fromX + (toX - fromX) * BUSY_AT}
        y={fromY + (toY - fromY) * BUSY_AT}
        angle={angle}
        text={`→${busyTo} ${formatBps(busyBps)}${pct(busyUtil)}`}
      />
      <EdgeRate
        x={fromX + (toX - fromX) * QUIET_AT}
        y={fromY + (toY - fromY) * QUIET_AT}
        angle={angle}
        quiet
        side={-1}
        text={`→${quietTo} ${formatBps(quietBps)}${pct(quietUtil)}`}
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
  quiet,
  side = 1,
}: {
  x: number;
  y: number;
  angle: number;
  text: string;
  /** The reverse direction: present but visually subordinate. */
  quiet?: boolean;
  /** Which side of the line to sit on. The two directions take opposite
   * sides, which is what keeps them apart on a short link — offsetting both
   * the same way stacked "7.2 Kbps" and "1 bps" into "0%1 bps". */
  side?: 1 | -1;
}) {
  // Perpendicular to the line. On a steep link that perpendicular is nearly
  // horizontal, and a centred label straddles the band — its halo then paints
  // over the arrowhead, which is the one thing that has to stay readable. So
  // steep links get the text anchored fully to one side instead.
  const px = Math.sin(angle) * side;
  const py = -Math.cos(angle) * side;
  const steep = Math.abs(px) > 0.5;
  const off = steep ? 11 : 13;
  return (
    <text
      x={x + px * off}
      y={y + py * off}
      textAnchor={steep ? (px > 0 ? "start" : "end") : "middle"}
      dominantBaseline="middle"
      style={{
        fontSize: quiet ? 9 : 10,
        fill: "currentColor",
        paintOrder: "stroke",
        stroke: "var(--map-label-halo, #020617)",
        strokeWidth: 3,
        strokeLinejoin: "round",
        pointerEvents: "none",
      }}
      className={
        quiet
          ? "text-slate-400 dark:text-slate-500"
          : "text-slate-500 dark:text-slate-300"
      }
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
  // Parallel links between one pair of nodes would draw the same path and be
  // indistinguishable — a second WAN circuit, a LAG member or a second tunnel
  // is a real thing to map, and until it is separated it looks like one link
  // and only the topmost can be clicked. Each coincident link gets a lane, and
  // the edge offsets itself perpendicular to the line by it.
  //
  // The bundle key includes the handles, because two links attached to
  // different sides of a node already diverge geometrically and should be left
  // alone. It is direction-agnostic: A→B and B→A drawn between the same sides
  // land on the same path and belong in the same bundle.
  const bundleKey = (l: MapLink) => {
    const a = `${l.from}:${l.from_handle ?? ""}`;
    const b = `${l.to}:${l.to_handle ?? ""}`;
    return a <= b ? `${a}|${b}` : `${b}|${a}`;
  };
  const bundleSize = new Map<string, number>();
  for (const l of def.links ?? []) {
    bundleSize.set(bundleKey(l), (bundleSize.get(bundleKey(l)) ?? 0) + 1);
  }
  const laneTaken = new Map<string, number>();

  const edges = (def.links ?? []).map((l) => {
    const lv = liveLinks.get(l.id);
    const key = bundleKey(l);
    const lane = laneTaken.get(key) ?? 0;
    laneTaken.set(key, lane + 1);
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
        lane,
        lanes: bundleSize.get(key) ?? 1,
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
