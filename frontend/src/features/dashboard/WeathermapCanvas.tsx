// The dashboard's weathermap rendering, split out so that @xyflow/react is a
// separate chunk rather than part of the initial bundle.
//
// It is by far the heaviest dependency in the app, and before this it loaded on
// every page — including the login screen, where no map can be shown. The
// dashboard is the landing route, so a static import here put the whole graph
// library in front of first paint even for an operator whose layout has no
// weathermap panel at all.
//
// Default export: React.lazy resolves the default binding.
import { Background, ConnectionMode, ReactFlow } from "@xyflow/react";
import { edgeTypes, nodeTypes, toFlow } from "../maps/canvas";

export default function WeathermapCanvas({
  definition,
  live,
}: {
  definition: Parameters<typeof toFlow>[0];
  live: Parameters<typeof toFlow>[1];
}) {
  const flow = toFlow(definition, live);
  return (
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
  );
}
