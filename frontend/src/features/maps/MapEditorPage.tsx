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
import {
  useMapDef,
  usePublish,
  useSaveDraft,
  useSuggestions,
  type MapDefinition,
  type MapLink,
  type MapNode,
} from "./api";
import { applyPositions, edgeTypes, nodeTypes, toFlow } from "./canvas";
import { useDeviceInterfaces, useDevices } from "../../api/hooks";
import { Button, Card, Input, Select, cx } from "../../components/ui";
import { ApiError } from "../../api/client";

let nextId = 1;
const genId = (prefix: string) =>
  `${prefix}${Date.now().toString(36)}${nextId++}`;

export function MapEditorPage() {
  const { id = "" } = useParams();
  const loaded = useMapDef(id, "draft");
  const save = useSaveDraft(id);
  const publish = usePublish(id);
  const [def, setDef] = useState<MapDefinition | null>(null);
  const [selectedLink, setSelectedLink] = useState<string | null>(null);
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
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

  // Autosave: debounce 2s after the last change (doc 30 §3).
  //
  // Two things here were unreliable, and both are the kind that lose work
  // quietly rather than loudly.
  //
  // The effect used to depend on `save`, whose identity changes on every
  // render — so any re-render inside the two-second window cleared the pending
  // timer and started it again. While dragging a node, or with any other query
  // refetching underneath, the timer could be reset indefinitely and the draft
  // was never written. It now depends on the document and the dirty flag
  // alone, with the mutation reached through a ref.
  //
  // And `dirty` was cleared when the save was *dispatched* rather than when it
  // succeeded, so a rejected save left the indicator reading "draft saved"
  // over unsaved work, with nothing to retry it.
  const saveRef = useRef(save);
  saveRef.current = save;
  const saveNow = useCallback(() => {
    if (!def) return;
    saveRef.current.mutate(def, { onSuccess: () => setDirty(false) });
  }, [def]);

  useEffect(() => {
    if (!dirty || !def) return;
    const t = setTimeout(saveNow, 2000);
    return () => clearTimeout(t);
  }, [def, dirty, saveNow]);

  // A draft still inside its debounce window is lost on navigation, and the
  // browser is the only thing that can ask. Cheap, and the alternative is
  // someone rearranging a map for ten minutes and closing the tab.
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const flow = useMemo(
    () => (def ? toFlow(def) : { nodes: [], edges: [] }),
    [def],
  );
  const [rfNodes, setRfNodes] = useState(flow.nodes);
  useEffect(() => setRfNodes(flow.nodes), [flow.nodes]);

  // Edges carry their selected flag from our own state rather than relying on
  // React Flow's internal selection. toFlow rebuilds every edge object from
  // the definition on each render, which wiped that internal flag — so the
  // Delete key had nothing selected to remove, and only the side panel button
  // worked.
  const rfEdges = useMemo(
    () => flow.edges.map((e) => ({ ...e, selected: e.id === selectedLink })),
    [flow.edges, selectedLink],
  );

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
      <div className="mb-2 flex flex-wrap items-center gap-3">
        <h1 className="text-lg font-semibold">Map editor</h1>
        <span
          className={cx(
            "text-xs",
            save.isError
              ? "text-red-500"
              : dirty || save.isPending
                ? "text-amber-500"
                : "text-slate-500",
          )}
        >
          {save.isError
            ? "not saved — changes are still here, press Save draft"
            : save.isPending
              ? "saving…"
              : dirty
                ? "unsaved changes"
                : "draft saved"}
        </span>
        <div className="flex-1" />
        <Link to={`/maps/${id}`}>
          <Button variant="ghost">View live</Button>
        </Link>
        {/* An explicit save, because a timer is a promise the user cannot
            see. Enabled even when nothing is pending: someone who has just
            moved a node and wants it written now should not have to work out
            whether the debounce already fired. */}
        <Button
          variant="ghost"
          disabled={save.isPending}
          onClick={saveNow}
          title="Write the draft now instead of waiting for autosave"
        >
          {save.isPending ? "Saving…" : "Save draft"}
        </Button>
        <Button onClick={() => publish.mutate()} disabled={publish.isPending}>
          {publish.isPending ? "Publishing…" : "Publish"}
        </Button>
      </div>
      {publishError && (
        <div className="mb-2 text-sm text-red-500">{publishError}</div>
      )}
      {/* Autosave runs on a timer, so a rejected save has no click to attach an
          error to — without this the map would just quietly stop persisting. */}
      {save.isError && (
        <div className="mb-2 text-sm text-red-500">
          Draft not saved: {(save.error as Error).message}
        </div>
      )}
      {/* Editing is desktop-only (doc 30, NFR-60): dragging nodes and a fixed
          side panel do not survive a phone. Say so plainly and point at the
          viewer, which is fully usable there. */}
      <div className="md:hidden">
        <Card title="Editing needs a wider screen">
          <p className="text-sm text-slate-600 dark:text-slate-400">
            The map editor is desktop-only — placing nodes and dragging links
            needs room and a pointer. The live map itself works fine here.
          </p>
          <div className="mt-3">
            <Link to={`/maps/${id}`}>
              <Button>View the live map</Button>
            </Link>
          </div>
        </Card>
      </div>

      <div className="hidden min-h-0 flex-1 gap-3 md:flex">
        <div className="min-h-0 flex-1 rounded-lg border border-slate-200 dark:border-slate-800">
          <ReactFlow
            nodes={rfNodes}
            edges={rfEdges}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            onNodesChange={onNodesChange}
            onConnect={onConnect}
            connectionMode={ConnectionMode.Loose}
            onEdgeClick={(_e, edge) => {
              setSelectedLink(edge.id);
              setSelectedNode(null);
            }}
            onNodeClick={(_e, node) => {
              setSelectedNode(node.id);
              setSelectedLink(null);
            }}
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
          selectedNode={selectedNode}
          onNodeRemoved={() => setSelectedNode(null)}
          onLinkRemoved={() => setSelectedLink(null)}
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
  selectedNode,
  onNodeRemoved,
  onLinkRemoved,
}: {
  def: MapDefinition;
  change: (fn: (d: MapDefinition) => MapDefinition) => void;
  mapID: string;
  selectedLink: string | null;
  selectedNode: string | null;
  onNodeRemoved: () => void;
  onLinkRemoved: () => void;
}) {
  const devices = useDevices({});
  const suggestions = useSuggestions(mapID);
  const link = def.links.find((l) => l.id === selectedLink);
  const node = def.nodes.find((n) => n.id === selectedNode);

  // New nodes land on a loose grid so they never stack exactly on top of an
  // existing one and vanish.
  const nextSpot = (d: MapDefinition) => ({
    x: 80 + (d.nodes.length % 5) * 160,
    y: 80 + Math.floor(d.nodes.length / 5) * 120,
  });

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
          ...nextSpot(d),
        },
      ],
    }));
  };

  // Nodes that stand for something NetInv does not poll — an ISP, a customer
  // site, a plain caption (FR-MAP-02). They carry no device binding, so they
  // never take a live state and render muted.
  const addPlain = (kind: MapNode["kind"], label: string) =>
    change((d) => ({
      ...d,
      nodes: [...d.nodes, { id: genId("n"), kind, label, ...nextSpot(d) }],
    }));

  return (
    <div className="flex w-72 shrink-0 flex-col gap-3 overflow-auto">
      <Card title="Add device node">
        <Select
          className="w-full"
          value=""
          onChange={(e) => addDevice(e.target.value)}
        >
          <option value="">Pick a device…</option>
          {devices.data?.data.map((d) => (
            <option key={d.id} value={d.id}>
              {d.name}
            </option>
          ))}
        </Select>
        <div className="mt-2 text-xs text-slate-500">
          Drag from a dot on any side of one node to any side of another to draw
          a link, then bind its interface below. Delete removes selected items.
        </div>
        <div className="mt-3 border-t border-slate-200 pt-3 dark:border-slate-800">
          <div className="mb-1.5 text-xs text-slate-500">
            Or place something NetInv doesn't poll:
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="ghost"
              onClick={() => addPlain("cloud", "Internet")}
            >
              ☁ Cloud
            </Button>
            <Button variant="ghost" onClick={() => addPlain("site", "Site")}>
              ▣ Site
            </Button>
            <Button variant="ghost" onClick={() => addPlain("label", "Label")}>
              Label
            </Button>
          </div>
        </div>
      </Card>

      {node && (
        <NodePanel
          key={node.id}
          node={node}
          change={change}
          onRemoved={onNodeRemoved}
        />
      )}

      {link && (
        <LinkPanel
          key={link.id}
          def={def}
          link={link}
          change={change}
          onRemoved={onLinkRemoved}
        />
      )}

      <Card title="LLDP suggestions">
        <div className="flex flex-col gap-1 text-xs">
          {suggestions.data?.data.length === 0 && (
            <span className="text-slate-500">
              No adjacencies discovered yet.
            </span>
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

// Renaming matters most for plain nodes, where the label is the entire
// content — an "Internet" cloud called "Label" is useless.
function NodePanel({
  node,
  change,
  onRemoved,
}: {
  node: MapNode;
  change: (fn: (d: MapDefinition) => MapDefinition) => void;
  onRemoved: () => void;
}) {
  const kindWord =
    node.kind === "device"
      ? "Device"
      : node.kind === "cloud"
        ? "Cloud"
        : node.kind === "site"
          ? "Site"
          : "Label";
  return (
    <Card title={`${kindWord} node`}>
      <label className="flex flex-col gap-1">
        <span className="text-xs text-slate-500">
          {node.kind === "device"
            ? "Label on the map — the device itself is not renamed"
            : "Label"}
        </span>
        <Input
          value={node.label ?? ""}
          onChange={(e) =>
            change((d) => ({
              ...d,
              nodes: d.nodes.map((n) =>
                n.id === node.id ? { ...n, label: e.target.value } : n,
              ),
            }))
          }
        />
      </label>
      <div className="mt-3 flex justify-end">
        <Button
          variant="danger"
          onClick={() => {
            change((d) => ({
              ...d,
              nodes: d.nodes.filter((n) => n.id !== node.id),
              // Links dangling off a removed node would never render.
              links: d.links.filter(
                (l) => l.from !== node.id && l.to !== node.id,
              ),
            }));
            onRemoved();
          }}
        >
          Remove from map
        </Button>
      </div>
    </Card>
  );
}

function LinkPanel({
  def,
  link,
  change,
  onRemoved,
}: {
  def: MapDefinition;
  link: MapLink;
  change: (fn: (d: MapDefinition) => MapDefinition) => void;
  onRemoved: () => void;
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

  // Utilisation colouring divides by capacity, resolved most specific first:
  // an explicit value here, else the A-side ifSpeed, else the slower of the
  // two sites' uplink rates. Tunnels report no ifSpeed, which is what the
  // uplink fallback is for (FR-MAP-08).
  const aIf = aIfs.data?.data.find(
    (i) => i.if_index === link.a_endpoint?.if_index,
  );
  const reportedSpeed = aIf?.speed_bps ?? 0;
  const devices = useDevices({});
  const wanOf = (nodeDeviceID?: string) =>
    devices.data?.data.find((d) => d.id === nodeDeviceID)?.wan_capacity_bps ??
    0;
  const wanA = wanOf(fromNode?.device_id);
  const wanB = wanOf(toNode?.device_id);
  const derivedFromWAN =
    wanA > 0 && wanB > 0
      ? Math.min(wanA, wanB)
      : wanA > 0 && !toNode?.device_id
        ? wanA
        : 0;

  const inherited =
    reportedSpeed > 0
      ? `interface reports ${Math.round(reportedSpeed / 1e6)}`
      : derivedFromWAN > 0
        ? `${Math.round(derivedFromWAN / 1e6)} from the slower site uplink`
        : "no interface speed and no site uplink rate set — leave blank and set the uplink on each gateway, or enter a value here";

  return (
    <Card title={`Link ${fromNode?.label ?? "?"} → ${toNode?.label ?? "?"}`}>
      <div className="flex flex-col gap-2 text-sm">
        {/* Either end will do. Binding one is enough — a link to something
            NetInv cannot poll still reads correctly from the far side. */}
        <p className="text-xs text-slate-500">
          Bind at least one end. The rates are read from the A side when it is
          bound, otherwise from B — a link to a node NetInv can't poll still
          shows traffic from whichever end it can.
        </p>
        {fromNode?.device_id && (
          <label className="flex flex-col gap-1">
            <span className="text-xs text-slate-500">
              A side — {fromNode.label} interface
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
            Capacity (Mbit/s){link.a_endpoint && ` — ${inherited}`}
          </span>
          <Input
            type="number"
            min={0}
            placeholder={
              reportedSpeed > 0
                ? String(Math.round(reportedSpeed / 1e6))
                : derivedFromWAN > 0
                  ? String(Math.round(derivedFromWAN / 1e6))
                  : "e.g. 100"
            }
            value={link.bandwidth_bps ? link.bandwidth_bps / 1e6 : ""}
            onChange={(e) => {
              const mbps = Number(e.target.value);
              patch({
                bandwidth_bps:
                  e.target.value && mbps > 0 ? mbps * 1e6 : undefined,
              });
            }}
          />
        </label>
      </div>
      <div className="mt-3 flex items-center justify-between gap-2">
        <span className="text-xs text-slate-500">
          Or press Delete with the link selected.
        </span>
        <Button
          variant="danger"
          onClick={() => {
            change((d) => ({
              ...d,
              links: d.links.filter((l) => l.id !== link.id),
            }));
            onRemoved();
          }}
        >
          Remove link
        </Button>
      </div>
    </Card>
  );
}
