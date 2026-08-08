import { useState } from "react";
import { Link } from "react-router-dom";
import { useCreateMap, useDeleteMap, useMaps, type MapMeta } from "./api";
import { Button, Card, EmptyState, Input } from "../../components/ui";
import { hasPermissionRole, useAuthStore } from "../auth/store";

export function MapsListPage() {
  const maps = useMaps();
  const create = useCreateMap();
  const [name, setName] = useState("");
  const [deleting, setDeleting] = useState<MapMeta | null>(null);
  // maps:write belongs to admin and operator; without it these buttons would
  // only ever produce a 403.
  const canEdit = hasPermissionRole(useAuthStore((s) => s.user), "operator");

  return (
    <div className="mx-auto max-w-4xl">
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-xl font-semibold">Weathermaps</h1>
        <div className={canEdit ? "flex gap-2" : "hidden"}>
          <Input
            placeholder="New map name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Button
            disabled={!name || create.isPending}
            onClick={() => {
              create.mutate(name);
              setName("");
            }}
          >
            Create
          </Button>
        </div>
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        {maps.data?.data.length === 0 && (
          <Card>
            <EmptyState>
              No maps yet. Create one and start placing devices.
            </EmptyState>
          </Card>
        )}
        {maps.data?.data.map((m) => (
          <Card key={m.id}>
            <div className="flex items-center justify-between">
              <div>
                <div className="font-medium">{m.name}</div>
                <div className="text-xs text-slate-500">
                  {m.published_rev > 0
                    ? `published rev ${m.published_rev}`
                    : "never published"}
                  {" · updated "}
                  {new Date(m.updated_at).toLocaleString()}
                </div>
              </div>
              <div className="flex gap-2">
                <Link to={`/maps/${m.id}`}>
                  <Button variant="ghost">View</Button>
                </Link>
                <Link to={`/maps/${m.id}/edit`}>
                  <Button variant="ghost">Edit</Button>
                </Link>
                {canEdit && (
                  <Button variant="danger" onClick={() => setDeleting(m)}>
                    Delete
                  </Button>
                )}
              </div>
            </div>
          </Card>
        ))}
      </div>
      {deleting && (
        <ConfirmDeleteMap map={deleting} onClose={() => setDeleting(null)} />
      )}
    </div>
  );
}

// Deleting a map destroys every revision of it, published and draft alike, and
// nothing else references them — so this follows the same typed-name
// confirmation the inventory uses for permanent device removal (doc 30 §0).
function ConfirmDeleteMap({
  map,
  onClose,
}: {
  map: MapMeta;
  onClose: () => void;
}) {
  const del = useDeleteMap();
  const [typed, setTyped] = useState("");
  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50">
      <Card title="Delete weathermap" className="w-[26rem]">
        <p className="text-sm">
          This removes <span className="font-medium">{map.name}</span> and every
          revision of it — the published map and all draft history. It cannot be
          undone.
        </p>
        <p className="mt-2 text-xs text-slate-500">
          Devices, interfaces and collected metrics are untouched; only the
          drawing is deleted. Anyone with the map's link will get a not-found.
        </p>
        <label className="mt-3 block text-xs text-slate-500">
          Type the map name to confirm:
          <Input
            className="mt-1 w-full"
            value={typed}
            autoFocus
            onChange={(e) => setTyped(e.target.value)}
            placeholder={map.name}
          />
        </label>
        {del.isError && (
          <div className="mt-2 text-sm text-red-500">
            {(del.error as Error).message}
          </div>
        )}
        <div className="mt-3 flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={typed !== map.name || del.isPending}
            onClick={() => del.mutate(map.id, { onSuccess: onClose })}
          >
            {del.isPending ? "Deleting…" : "Delete permanently"}
          </Button>
        </div>
      </Card>
    </div>
  );
}
