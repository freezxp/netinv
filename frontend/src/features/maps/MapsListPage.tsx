import { useState } from "react";
import { Link } from "react-router-dom";
import { useCreateMap, useMaps } from "./api";
import { Button, Card, EmptyState, Input } from "../../components/ui";

export function MapsListPage() {
  const maps = useMaps();
  const create = useCreateMap();
  const [name, setName] = useState("");

  return (
    <div className="mx-auto max-w-4xl">
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-xl font-semibold">Weathermaps</h1>
        <div className="flex gap-2">
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
              </div>
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}
