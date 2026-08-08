// Platform management (doc 30 §8): sites, poller fleet, connector catalog,
// credentials vault.
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import { useSites } from "../../api/hooks";
import { useAuthStore, hasPermissionRole } from "../auth/store";
import { DiscoveryTab } from "./DiscoveryTab";
import {
  Button,
  Card,
  EmptyState,
  Input,
  Select,
  StatusBadge,
  cx,
} from "../../components/ui";
import { formatDuration } from "../../lib/format";

const tabs = ["Sites", "Discovery", "Pollers", "Connectors", "Credentials"] as const;

export function PlatformPage() {
  const [tab, setTab] = useState<(typeof tabs)[number]>("Sites");
  const user = useAuthStore((s) => s.user);
  const visible = tabs.filter(
    (t) => t !== "Credentials" || hasPermissionRole(user),
  );
  return (
    <div className="mx-auto max-w-5xl">
      <h1 className="mb-3 text-xl font-semibold">Platform</h1>
      <div className="mb-4 flex gap-1 border-b border-slate-200 dark:border-slate-800">
        {visible.map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={cx(
              "px-3 py-2 text-sm",
              t === tab
                ? "border-b-2 border-sky-500 font-medium text-sky-500"
                : "text-slate-500",
            )}
          >
            {t}
          </button>
        ))}
      </div>
      {tab === "Sites" && <SitesTab />}
      {tab === "Discovery" && <DiscoveryTab />}
      {tab === "Pollers" && <PollersTab />}
      {tab === "Connectors" && <ConnectorsTab />}
      {tab === "Credentials" && <CredentialsTab />}
    </div>
  );
}

function SitesTab() {
  const sites = useSites();
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: () =>
      api("/sites", { method: "POST", body: JSON.stringify({ name }) }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["sites"] });
      setName("");
    },
  });
  return (
    <div className="flex flex-col gap-3">
      <Card>
        <div className="flex gap-2">
          <Input
            placeholder="New site name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Button disabled={!name} onClick={() => create.mutate()}>
            Add site
          </Button>
        </div>
      </Card>
      <Card className="p-0">
        <table className="w-full text-sm">
          <tbody>
            {sites.data?.data.map((s) => (
              <tr
                key={s.id}
                className="border-b border-slate-100 dark:border-slate-800/60"
              >
                <td className="px-4 py-2 font-medium">{s.name}</td>
                <td className="px-4 py-2 text-slate-500">{s.location || "—"}</td>
                <td className="mono px-4 py-2 text-xs text-slate-500">{s.id}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </div>
  );
}

interface Poller {
  id: string;
  name: string;
  site_id: string;
  status: string;
  version: string;
  last_heartbeat_at: string | null;
  stats: Record<string, number>;
}

function PollersTab() {
  const qc = useQueryClient();
  const pollers = useQuery({
    queryKey: ["pollers"],
    queryFn: () => api<{ data: Poller[] }>("/pollers"),
    refetchInterval: 15_000,
  });
  const sites = useSites();
  const [name, setName] = useState("");
  const [siteID, setSiteID] = useState("");
  const [issued, setIssued] = useState<string | null>(null);
  const issue = useMutation({
    mutationFn: () =>
      api<{ token: string }>("/pollers/enroll-tokens", {
        method: "POST",
        body: JSON.stringify({ name, site_id: siteID }),
      }),
    onSuccess: (r) => {
      setIssued(r.token);
      qc.invalidateQueries({ queryKey: ["pollers"] });
    },
  });
  const action = useMutation({
    mutationFn: ({ id, verb }: { id: string; verb: string }) =>
      api(`/pollers/${id}/${verb}`, { method: "POST" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["pollers"] }),
  });
  const hbAge = (p: Poller) =>
    p.last_heartbeat_at
      ? formatDuration((Date.now() - Date.parse(p.last_heartbeat_at)) / 1000) +
        " ago"
      : "never";
  return (
    <div className="flex flex-col gap-3">
      <Card title="Enroll a new poller">
        <div className="flex gap-2">
          <Input
            placeholder="Poller name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Select value={siteID} onChange={(e) => setSiteID(e.target.value)}>
            <option value="">Site…</option>
            {sites.data?.data.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </Select>
          <Button disabled={!name || !siteID} onClick={() => issue.mutate()}>
            Issue token
          </Button>
        </div>
        {issued && (
          <div className="mono mt-2 rounded bg-slate-100 p-2 text-xs dark:bg-slate-800">
            One-time enrollment token (15 min):{" "}
            <span className="select-all">{issued}</span>
          </div>
        )}
      </Card>
      <Card className="p-0">
        <table className="w-full text-sm">
          <tbody>
            {pollers.data?.data.map((p) => (
              <tr
                key={p.id}
                className="border-b border-slate-100 dark:border-slate-800/60"
              >
                <td className="px-4 py-2">
                  <StatusBadge status={p.status} />
                </td>
                <td className="px-4 py-2 font-medium">{p.name}</td>
                <td className="px-4 py-2 text-slate-500">hb {hbAge(p)}</td>
                <td className="px-4 py-2 text-xs text-slate-500">
                  ok {p.stats?.polls_ok ?? 0} · fail {p.stats?.polls_failed ?? 0}{" "}
                  · buf {p.stats?.buffer_depth ?? 0}
                </td>
                <td className="px-4 py-2 text-right">
                  {p.status === "pending" && (
                    <Button
                      variant="ghost"
                      onClick={() => action.mutate({ id: p.id, verb: "approve" })}
                    >
                      Approve
                    </Button>
                  )}
                  {p.status === "active" && (
                    <Button
                      variant="ghost"
                      onClick={() => action.mutate({ id: p.id, verb: "disable" })}
                    >
                      Disable
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {pollers.data?.data.length === 0 && (
          <EmptyState>No pollers enrolled yet.</EmptyState>
        )}
      </Card>
    </div>
  );
}

interface Connector {
  id: string;
  vendor: string;
  display_name: string;
  version: string;
  capabilities: string[];
  device_count: number;
}

function ConnectorsTab() {
  const connectors = useQuery({
    queryKey: ["connectors"],
    queryFn: () => api<{ data: Connector[] }>("/connectors"),
  });
  return (
    <div className="grid gap-3 md:grid-cols-2">
      {connectors.data?.data.map((c) => (
        <Card key={c.id}>
          <div className="flex items-baseline justify-between">
            <span className="font-medium">{c.display_name}</span>
            <span className="mono text-xs text-slate-500">{c.version}</span>
          </div>
          <div className="mt-1 flex flex-wrap gap-1">
            {c.capabilities.map((cap) => (
              <span
                key={cap}
                className="rounded bg-sky-600/10 px-1.5 py-0.5 text-xs text-sky-500"
              >
                {cap}
              </span>
            ))}
          </div>
          <div className="mt-2 text-xs text-slate-500">
            {c.device_count} device{c.device_count === 1 ? "" : "s"}
          </div>
        </Card>
      ))}
    </div>
  );
}

interface Credential {
  id: string;
  name: string;
  kind: string;
  meta: Record<string, string>;
  device_count: number;
}

function CredentialsTab() {
  const qc = useQueryClient();
  const creds = useQuery({
    queryKey: ["credentials"],
    queryFn: () => api<{ data: Credential[] }>("/credentials"),
  });
  const [name, setName] = useState("");
  const [community, setCommunity] = useState("");
  const create = useMutation({
    mutationFn: () =>
      api("/credentials", {
        method: "POST",
        body: JSON.stringify({
          name,
          kind: "snmp_v2c",
          secret: { community },
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["credentials"] });
      setName("");
      setCommunity("");
    },
  });
  return (
    <div className="flex flex-col gap-3">
      <Card title="Add SNMPv2c credential (v3 via API)">
        <div className="flex gap-2">
          <Input
            placeholder="Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Input
            type="password"
            placeholder="Community (write-only)"
            value={community}
            onChange={(e) => setCommunity(e.target.value)}
          />
          <Button disabled={!name || !community} onClick={() => create.mutate()}>
            Add
          </Button>
        </div>
      </Card>
      <Card className="p-0">
        <table className="w-full text-sm">
          <tbody>
            {creds.data?.data.map((c) => (
              <tr
                key={c.id}
                className="border-b border-slate-100 dark:border-slate-800/60"
              >
                <td className="px-4 py-2 font-medium">{c.name}</td>
                <td className="px-4 py-2 text-slate-500">{c.kind}</td>
                <td className="px-4 py-2 text-slate-500">
                  {c.meta.username ?? "•••"}
                </td>
                <td className="px-4 py-2 text-xs text-slate-500">
                  {c.device_count} devices
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </div>
  );
}
