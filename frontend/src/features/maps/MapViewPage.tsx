// Live weathermap viewer (doc 30 §3): published definition + ≤30s live
// coloring; every viewer shares the server-side cached payload.
import { useMemo } from "react";
import { Link, useParams } from "react-router-dom";
import { ReactFlow, Background, Controls } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { useMapDef, useMapLive, utilLegend } from "./api";
import { nodeTypes, toFlow } from "./canvas";
import { Button } from "../../components/ui";

export function MapViewPage() {
  const { id = "" } = useParams();
  const def = useMapDef(id, "published");
  const live = useMapLive(id, !!def.data);

  const flow = useMemo(
    () =>
      def.data
        ? toFlow(def.data.definition, live.data)
        : { nodes: [], edges: [] },
    [def.data, live.data],
  );

  return (
    <div className="flex h-full flex-col">
      <div className="mb-2 flex items-center gap-3">
        <h1 className="text-lg font-semibold">Weathermap</h1>
        <span className="text-xs text-slate-500">
          {live.data ? `as of ${new Date(live.data.as_of).toLocaleTimeString()}` : "…"}
        </span>
        <div className="flex-1" />
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
    </div>
  );
}
