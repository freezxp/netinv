// Personal API tokens (doc 09 §3, doc 20 §2): the credential another
// application uses to read inventory and flow. Each user manages their own.
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import { Button, Card, EmptyState, Input, Select } from "../../components/ui";

interface ApiToken {
  id: string;
  name: string;
  scopes: string[];
  expires_at: string | null;
  last_used_at: string | null;
  revoked_at: string | null;
  created_at: string;
}

// Read scopes first: an integration reading inventory and flow needs only
// these, and they are what a token should usually carry. The server refuses
// any scope its creator does not hold, so offering the rest costs nothing.
const readScopes = ["devices:read", "metrics:read", "maps:read", "alerts:read"];
const otherScopes = [
  "platform:read",
  "audit:read",
  "devices:write",
  "maps:write",
  "alerts:ack",
  "exports:run",
];

const lifetimes = [
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 365, label: "1 year" },
  { days: 0, label: "Never expires" },
];

function when(iso: string | null) {
  return iso ? new Date(iso).toLocaleString() : "—";
}

export function ApiTokensTab() {
  const qc = useQueryClient();
  const tokens = useQuery({
    queryKey: ["api-tokens"],
    queryFn: () => api<{ data: ApiToken[] }>("/users/me/tokens"),
  });

  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>([
    "devices:read",
    "metrics:read",
  ]);
  const [days, setDays] = useState(90);
  // The plain value exists in this one response and nowhere else; once the
  // operator navigates away it is gone, which the card says.
  const [minted, setMinted] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () =>
      api<ApiToken & { token: string }>("/users/me/tokens", {
        method: "POST",
        body: JSON.stringify({
          name,
          scopes,
          expires_at: days
            ? new Date(Date.now() + days * 86400_000).toISOString()
            : null,
        }),
      }),
    onSuccess: (t) => {
      setMinted(t.token);
      setName("");
      qc.invalidateQueries({ queryKey: ["api-tokens"] });
    },
  });

  const revoke = useMutation({
    mutationFn: (id: string) =>
      api(`/users/me/tokens/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["api-tokens"] }),
  });

  const toggle = (s: string) =>
    setScopes((cur) =>
      cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s],
    );

  const scopeBox = (s: string) => (
    <label key={s} className="flex items-center gap-1.5 text-sm">
      <input
        type="checkbox"
        checked={scopes.includes(s)}
        onChange={() => toggle(s)}
      />
      <code className="text-xs">{s}</code>
    </label>
  );

  return (
    <div className="flex flex-col gap-3">
      <Card title="Create API token">
        <div className="flex flex-col gap-3">
          <p className="text-sm text-slate-500">
            Send as <code>Authorization: Bearer nv_…</code>. A token can do only
            what its scopes allow <em>and</em> your own role allows, and stops
            working if your account is deactivated. Inventory needs{" "}
            <code>devices:read</code>; flow (<code>/flow/top</code>,{" "}
            <code>/flow/series</code>) needs <code>metrics:read</code>.
          </p>
          <div className="flex flex-wrap gap-2">
            <Input
              placeholder="Name, e.g. the application using it"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="min-w-64 flex-1"
            />
            <Select
              value={days}
              onChange={(e) => setDays(Number(e.target.value))}
            >
              {lifetimes.map((l) => (
                <option key={l.days} value={l.days}>
                  {l.label}
                </option>
              ))}
            </Select>
          </div>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {readScopes.map(scopeBox)}
          </div>
          <details>
            <summary className="cursor-pointer text-xs text-slate-500">
              Other scopes
            </summary>
            <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1">
              {otherScopes.map(scopeBox)}
            </div>
          </details>
          {create.isError && (
            <div className="text-sm text-red-500">
              {(create.error as Error).message}
            </div>
          )}
          <div>
            <Button
              disabled={!name.trim() || scopes.length === 0 || create.isPending}
              onClick={() => create.mutate()}
            >
              Create token
            </Button>
          </div>
          {minted && (
            <div className="rounded-sm border border-amber-400 bg-amber-50 p-3 text-sm dark:bg-amber-950/40">
              <div className="mb-1 font-medium">
                Copy this token now — it will not be shown again.
              </div>
              <div className="flex items-center gap-2">
                <code className="flex-1 break-all">{minted}</code>
                <Button
                  variant="ghost"
                  onClick={() => navigator.clipboard?.writeText(minted)}
                >
                  Copy
                </Button>
                <Button variant="ghost" onClick={() => setMinted(null)}>
                  Done
                </Button>
              </div>
            </div>
          )}
        </div>
      </Card>

      <Card title="Your tokens">
        {tokens.data && tokens.data.data.length === 0 ? (
          <EmptyState>No API tokens yet.</EmptyState>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-slate-500">
              <tr>
                <th className="py-1">Name</th>
                <th>Scopes</th>
                <th>Last used</th>
                <th>Expires</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {tokens.data?.data.map((t) => (
                <tr
                  key={t.id}
                  className={
                    t.revoked_at ? "text-slate-400 line-through" : undefined
                  }
                >
                  <td className="py-1">{t.name}</td>
                  <td className="text-xs">{t.scopes.join(", ")}</td>
                  <td>{when(t.last_used_at)}</td>
                  <td>{t.expires_at ? when(t.expires_at) : "never"}</td>
                  <td className="text-right">
                    {t.revoked_at ? (
                      <span className="text-xs no-underline">revoked</span>
                    ) : (
                      <Button
                        variant="danger"
                        disabled={revoke.isPending}
                        onClick={() => {
                          if (
                            confirm(
                              `Revoke "${t.name}"? Anything using it stops working.`,
                            )
                          )
                            revoke.mutate(t.id);
                        }}
                      >
                        Revoke
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
