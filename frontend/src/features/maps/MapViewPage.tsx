// Live weathermap viewer (doc 30 §3): published definition + ≤30s live
// coloring; every viewer shares the server-side cached payload.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ReactFlow, Background, ConnectionMode, Controls } from "@xyflow/react";
import { useMapDef, useMapLive, utilLegend } from "./api";
import { edgeTypes, nodeTypes, toFlow } from "./canvas";
import { LinkGraph } from "./LinkGraph";
import { NodeAlerts } from "./NodeAlerts";
import { useAlerts } from "../../api/hooks";
import { Button } from "../../components/ui";
import { RangePicker } from "../../components/RangePicker";

export function MapViewPage() {
  const { id = "" } = useParams();
  const def = useMapDef(id, "published");
  const live = useMapLive(id, !!def.data);

  // The card is pinned where the link was entered, not dragged along under the
  // cursor. Following the mouse made it impossible to reach: the moment you
  // moved toward the graph it moved too, and it could not be read from anyway
  // because it ignored pointer events. Pinned and interactive, uPlot's legend
  // reports the value under the cursor, which is the whole reason to look at a
  // graph on a map rather than a colour on a line.
  // One card at a time. Links and nodes share the slot: two overlapping cards
  // is not a state worth supporting, and it keeps one grace timer rather than
  // two that can each cancel the other's close.
  const [hover, setHover] = useState<{
    kind: "link" | "node";
    id: string;
    x: number;
    y: number;
  } | null>(null);

  // Leaving the link does not close the card immediately: there is a gap of
  // dead space between the line and the card, and closing on the first
  // mouseleave makes the card unreachable. The grace period is cancelled the
  // moment the pointer lands on the card itself.
  const closeTimer = useRef<number | null>(null);
  const cancelClose = useCallback(() => {
    if (closeTimer.current !== null) {
      clearTimeout(closeTimer.current);
      closeTimer.current = null;
    }
  }, []);
  const scheduleClose = useCallback(() => {
    cancelClose();
    closeTimer.current = window.setTimeout(() => setHover(null), 220);
  }, [cancelClose]);
  useEffect(() => cancelClose, [cancelClose]);

  // A pinned card needs a way out that does not involve finding its edge.
  useEffect(() => {
    if (!hover) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setHover(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [hover]);

  // The fleet-wide list rather than a per-device query on hover: it is already
  // polled every 15s for the alert panels, so react-query serves this from
  // cache and hovering costs no request.
  const alerts = useAlerts();
  const alertsByNode = useMemo(() => {
    const rows = alerts.data?.data ?? [];
    const byDevice = new Map<string, typeof rows>();
    for (const a of rows) {
      if (!a.device_id) continue;
      const list = byDevice.get(a.device_id);
      if (list) list.push(a);
      else byDevice.set(a.device_id, [a]);
    }
    const out = new Map<string, typeof rows>();
    for (const n of def.data?.definition.nodes ?? []) {
      const list = n.device_id ? byDevice.get(n.device_id) : undefined;
      if (list?.length) out.set(n.id, list);
    }
    return out;
  }, [alerts.data, def.data]);

  const flow = useMemo(
    () =>
      def.data
        ? toFlow(def.data.definition, live.data)
        : { nodes: [], edges: [] },
    [def.data, live.data],
  );

  return (
    <div className="flex h-full flex-col">
      <div className="mb-2 flex flex-wrap items-center gap-3">
        <h1 className="text-lg font-semibold">Weathermap</h1>
        <span className="text-xs text-slate-500">
          {live.data
            ? `as of ${new Date(live.data.as_of).toLocaleTimeString()}`
            : "…"}
        </span>
        <div className="flex-1" />
        {/* The link graph follows the shared range. The control lives on the
            page chrome rather than in the card so that changing the range does
            not mean keeping a card open while reaching for a picker inside it. */}
        <RangePicker ariaLabel="Link graph time range" />
        <div className="flex items-center gap-1 text-[10px] text-slate-500">
          {utilLegend.map(([max, color]) => (
            <span key={max} className="inline-flex items-center gap-0.5">
              <span className="h-2 w-3" style={{ background: color }} />
              {max}%
            </span>
          ))}
        </div>
        <Link to={`/maps/${id}/edit`}>
          <Button variant="ghost">Edit</Button>
        </Link>
      </div>
      <div className="min-h-0 flex-1 rounded-lg border border-slate-200 dark:border-slate-800">
        <ReactFlow
          nodes={flow.nodes}
          edges={flow.edges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onEdgeMouseEnter={(e, edge) => {
            cancelClose();
            // Only reposition when it is a different link: re-pinning on every
            // enter would make the card jump while the pointer crosses the same
            // line twice.
            setHover((prev) =>
              prev?.kind === "link" && prev.id === edge.id
                ? prev
                : { kind: "link", id: edge.id, x: e.clientX, y: e.clientY },
            );
          }}
          onEdgeMouseLeave={scheduleClose}
          onNodeMouseEnter={(e, node) => {
            // Only nodes that actually have something to report open a card.
            // A card that says "no alerts" on every healthy device turns the
            // whole map into a minefield of popups while you pan across it.
            if (!(alertsByNode.get(node.id)?.length ?? 0)) return;
            cancelClose();
            setHover((prev) =>
              prev?.kind === "node" && prev.id === node.id
                ? prev
                : { kind: "node", id: node.id, x: e.clientX, y: e.clientY },
            );
          }}
          onNodeMouseLeave={scheduleClose}
          // Must match the editor: nodes declare every handle as a source, and
          // under the default Strict mode edge rendering cannot resolve a
          // target handle — so links silently vanish from the published map.
          connectionMode={ConnectionMode.Loose}
          fitView
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable={false}
          proOptions={{ hideAttribution: true }}
        >
          <Background gap={16} />
          <Controls showInteractive={false} />
        </ReactFlow>
      </div>
      {hover?.kind === "link" && def.data && (
        <LinkGraph
          def={def.data.definition}
          live={live.data}
          linkID={hover.id}
          at={{ x: hover.x, y: hover.y }}
          onPointerEnter={cancelClose}
          onPointerLeave={scheduleClose}
          onClose={() => setHover(null)}
        />
      )}
      {hover?.kind === "node" && def.data && (
        <NodeAlerts
          def={def.data.definition}
          nodeID={hover.id}
          alerts={alertsByNode.get(hover.id) ?? []}
          at={{ x: hover.x, y: hover.y }}
          onPointerEnter={cancelClose}
          onPointerLeave={scheduleClose}
          onClose={() => setHover(null)}
        />
      )}
    </div>
  );
}
