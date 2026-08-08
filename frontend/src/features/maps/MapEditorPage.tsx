// Weathermap editor (doc 30 §3): place nodes, draw links, bind interface
// endpoints, autosave drafts, publish. Snapshot-stack undo (Ctrl+Z).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  ReactFlow,
  Background,
  ConnectionMode,
  Controls,
  applyNodeChanges,
  type Connection,
  type NodeChange,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  useMapDef,
  usePublish,
  useSaveDraft,
  useSuggestions,
  type MapDefinition,
  type MapLink,
} from "./api";
import { applyPositions, nodeTypes, toFlow } from "./canvas";
import { useDeviceInterfaces, useDevices } from "../../api/hooks";
import { Button, Card, Input, Select, cx } from "../../components/ui";
import { ApiError } from "../../api/client";

let nextId = 1;
const genId = (prefix: string) => `${prefix}${Date.now().toString(36)}${nextId++}`;

export function MapEditorPage() {
  const { id = "" } = useParams();
  const loaded = useMapDef(id, "draft");
  const save = useSaveDraft(id);
  const publish = usePublish(id);
  const [def, setDef] = useState<MapDefinition | null>(null);
  const [selectedLink, setSelectedLink] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const undoStack = useRef<MapDefinition[]>([]);

  useEffect(() => {
    if (loaded.data && !def) setDef(loaded.data.definition);
  }, [loaded.data, def]);

  // Mutate with undo snapshot (FR-MAP-02).
  const change = useCallback((fn: (d: MapDefinition) => MapDefinition) => {
    setDef((d) => {
      if (!d) return d;
      undoStack.current.push(d);
      if (undoStack.current.length > 50) undoStack.current.shift();
      return fn(d);
    });
    setDirty(true);
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "z") {
        e.preventDefault();
        const prev = undoStack.current.pop();
        if (prev) {
          setDef(prev);
          setDirty(true);
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Autosave: debounce 2s after last change (doc 30 §3).
  useEffect(() => {
    if (!dirty || !def) return;
    const t = setTimeout(() => {
      save.mutate(def);
      setDirty(false);
    }, 2000);
    return () => clearTimeout(t);
  }, [def, dirty, save]);

  const flow = useMemo(
    () => (def ? toFlow(def) : { nodes: [], edges: [] }),
    [def],
  );
  const [rfNodes, setRfNodes] = useState(flow.nodes);
  useEffect(() => setRfNodes(flow.nodes), [flow.nodes]);

  const onNodesChange = useCallback(
    (changes: NodeChange<(typeof flow.nodes)[number]>[]) => {
      setRfNodes((ns) => applyNodeChanges(changes, ns));
      // Persist positions on drag end.
      if (changes.some((c) => c.type === "position" && c.dragging === false)) {
        setRfNodes((ns) => {
          change((d) => applyPositions(d, ns as never));
          return ns;
        });
      }
      const removed = changes.filter((c) => c.type === "remove");
      if (removed.length) {
        const ids = new Set(removed.map((c) => c.id));
        change((d) => ({
          ...d,
          nodes: d.nodes.filter((n) => !ids.has(n.id)),
          links: d.links.filter((l) => !ids.has(l.from) && !ids.has(l.to)),
        }));
      }
    },
    [change],
  );

  const onConnect = useCallback(
    (conn: Connection) => {
      if (!conn.source || !conn.target || conn.source === conn.target) return;
      const linkId = genId("l");
      change((d) => ({
        ...d,
        links: [
          ...d.links,
          {
            id: linkId,
            from: conn.source,
            to: conn.target,
            from_handle: conn.sourceHandle,
            to_handle: conn.targetHandle,
          } satisfies MapLink,
        ],
      }));
      setSelectedLink(linkId);
    },
    [change],
  );

  const publishError =
    publish.error instanceof ApiError ? publish.error.message : null;

  if (!def) return <div className="text-slate-500">Loading map…</div>;

  return (
    <div className="flex h-full flex-col">
      <div className="mb-2 flex items-center gap-3">
        <h1 className="text-lg font-semibold">Map editor</h1>
        <span
          className={cx(
            "text-xs",
            dirty || save.isPending ? "text-amber-500" : "text-slate-500",
          )}
        >
          {save.isPending ? "saving…" : dirty ? "unsaved changes" : "draft saved"}
        </span>
        <div className="flex-1" />
        <Link to={`/maps/${id}`}>
          <Button variant="ghost">View live</Button>
        </Link>
        <Button onClick={() => publish.mutate()} disabled={publish.isPending}>
          {publish.isPending ? "Publishing…" : "Publish"}
        </Button>
      </div>
      {publishError && (
        <div className="mb-2 text-sm text-red-500">{publishError}</div>
      )}
      <div className="flex min-h-0 flex-1 gap-3">
        <div className="min-h-0 flex-1 rounded-lg border border-slate-200 dark:border-slate-800">
          <ReactFlow
            nodes={rfNodes}
            edges={flow.edges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onConnect={onConnect}
            connectionMode={ConnectionMode.Loose}
            onEdgeClick={(_e, edge) => setSelectedLink(edge.id)}
            onEdgesChange={(changes) => {
              const removed = changes.filter((c) => c.type === "remove");
              if (removed.length) {
                const ids = new Set(removed.map((c) => c.id));
                change((d) => ({
                  ...d,
                  links: d.links.filter((l) => !ids.has(l.id)),
                }));
              }
            }}
            fitView
            deleteKeyCode={["Delete", "Backspace"]}
            proOptions={{ hideAttribution: true }}
          >
            <Background gap={16} />
            <Controls />
          </ReactFlow>
        </div>
        <SidePanel
          def={def}
          change={change}
          mapID={id}
          selectedLink={selectedLink}
        />
      </div>
    </div>
  );
}

function SidePanel({
  def,
  change,
  mapID,
  selectedLink,
}: {
  def: MapDefinition;
  change: (fn: (d: MapDefinition) => MapDefinition) => void;
  mapID: string;
  selectedLink: string | null;
}) {
  const devices = useDevices({});
  const suggestions = useSuggestions(mapID);
  const link = def.links.find((l) => l.id === selectedLink);

  const addDevice = (deviceID: string) => {
    const dev = devices.data?.data.find((d) => d.id === deviceID);
    if (!dev || def.nodes.some((n) => n.device_id === deviceID)) return;
    change((d) => ({
      ...d,
      nodes: [
        ...d.nodes,
        {
          id: genId("n"),
          kind: "device",
          device_id: deviceID,
          label: dev.name,
          x: 80 + (d.nodes.length % 5) * 160,
          y: 80 + Math.floor(d.nodes.length / 5) * 120,
        },
      ],
    }));
  };

  return (
    <div className="flex w-72 shrink-0 flex-col gap-3 overflow-auto">
      <Card title="Add device node">
        <Select className="w-full" value="" onChange={(e) => addDevice(e.target.value)}>
          <option value="">Pick a device…</option>
          {devices.data?.data.map((d) => (
            <option key={d.id} value={d.id}>
              {d.name}
            </option>
          ))}
        </Select>
        <div className="mt-2 text-xs text-slate-500">
          Drag from a dot on any side of one node to any side of another to
          draw a link, then bind its interface below. Delete removes selected
          items.
        </div>
      </Card>

      {link && (
        <LinkPanel key={link.id} def={def} link={link} change={change} />
      )}

      <Card title="LLDP suggestions">
        <div className="flex flex-col gap-1 text-xs">
          {suggestions.data?.data.length === 0 && (
            <span className="text-slate-500">No adjacencies discovered yet.</span>
          )}
          {suggestions.data?.data.map((s, i) => (
            <div key={i} className="text-slate-600 dark:text-slate-400">
              {s.a_device} if {s.a_if_index} ⇄ {s.b_device || s.b_sysname} (
              {s.b_port})
            </div>
          ))}
        </div>
      </Card>
    </div>
  );
}

function LinkPanel({
  def,
  link,
  change,
}: {
  def: MapDefinition;
  link: MapLink;
  change: (fn: (d: MapDefinition) => MapDefinition) => void;
}) {
  const fromNode = def.nodes.find((n) => n.id === link.from);
  const toNode = def.nodes.find((n) => n.id === link.to);
  const aIfs = useDeviceInterfaces(fromNode?.device_id ?? "");
  const bIfs = useDeviceInterfaces(toNode?.device_id ?? "");

  const patch = (fields: Partial<MapLink>) =>
    change((d) => ({
      ...d,
      links: d.links.map((l) => (l.id === link.id ? { ...l, ...fields } : l)),
    }));

  const bind = (side: "a" | "b", deviceID: string, ifIndex: number) =>
    patch({
      [side === "a" ? "a_endpoint" : "b_endpoint"]: {
        device_id: deviceID,
        if_index: ifIndex,
      },
    });

  // Utilisation colouring divides by capacity. Most interfaces report ifSpeed,
  // but VPN tunnels and other virtual interfaces report 0 — for those the
  // operator has to say what the link is worth, or it sits at 0% forever.
  const aIf = aIfs.data?.data.find(
    (i) => i.if_index === link.a_endpoint?.if_index,
  );
  const reportedSpeed = aIf?.speed_bps ?? 0;

  return (
    <Card title={`Link ${fromNode?.label ?? "?"} → ${toNode?.label ?? "?"}`}>
      <div className="flex flex-col gap-2 text-sm">
        {fromNode?.device_id && (
          <label className="flex flex-col gap-1">
            <span className="text-xs text-slate-500">
              A side — {fromNode.label} interface (drives coloring)
            </span>
            <Select
              value={link.a_endpoint?.if_index ?? ""}
              onChange={(e) =>
                bind("a", fromNode.device_id!, Number(e.target.value))
              }
            >
              <option value="">unbound</option>
              {aIfs.data?.data.map((i) => (
                <option key={i.id} value={i.if_index}>
                  {i.name || `if ${i.if_index}`} {i.alias && `— ${i.alias}`}
                </option>
              ))}
            </Select>
          </label>
        )}
        {toNode?.device_id && (
          <label className="flex flex-col gap-1">
            <span className="text-xs text-slate-500">
              B side — {toNode.label} interface
            </span>
            <Select
              value={link.b_endpoint?.if_index ?? ""}
              onChange={(e) =>
                bind("b", toNode.device_id!, Number(e.target.value))
              }
            >
              <option value="">unbound</option>
              {bIfs.data?.data.map((i) => (
                <option key={i.id} value={i.if_index}>
                  {i.name || `if ${i.if_index}`} {i.alias && `— ${i.alias}`}
                </option>
              ))}
            </Select>
          </label>
        )}
        <label className="flex flex-col gap-1">
          <span className="text-xs text-slate-500">
            Capacity (Mbit/s)
            {link.a_endpoint &&
              (reportedSpeed > 0
                ? ` — interface reports ${Math.round(reportedSpeed / 1e6)}`
                : " — interface reports no speed, set one to get utilisation colour")}
          </span>
          <Input
            type="number"
            min={0}
            placeholder={
              reportedSpeed > 0 ? String(Math.round(reportedSpeed / 1e6)) : "e.g. 100"
            }
            value={link.bandwidth_bps ? link.bandwidth_bps / 1e6 : ""}
            onChange={(e) => {
              const mbps = Number(e.target.value);
              patch({
                bandwidth_bps: e.target.value && mbps > 0 ? mbps * 1e6 : undefined,
              });
            }}
          />
        </label>
      </div>
    </Card>
  );
}
