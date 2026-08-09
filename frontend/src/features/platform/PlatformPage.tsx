// Platform management (doc 30 §8): sites, poller fleet, connector catalog,
// credentials vault.
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import { useSites } from "../../api/hooks";
import { useAuthStore, hasPermissionRole } from "../auth/store";
import { DiscoveryTab } from "./DiscoveryTab";
import { CapacityTab } from "./CapacityTab";
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

const tabs = [
  "Sites",
  "Discovery",
  "Pollers",
  "Connectors",
  "Credentials",
  "Capacity",
] as const;

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
      {tab === "Capacity" && <CapacityTab />}
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
      <Card className="overflow-x-auto p-0">
        <table className="w-full min-w-[32rem] text-sm">
          <tbody>
            {sites.data?.data.map((s) => (
              <tr
                key={s.id}
                className="border-b border-slate-100 dark:border-slate-800/60"
              >
                <td className="px-4 py-2 font-medium">{s.name}</td>
                <td className="px-4 py-2 text-slate-500">
                  {s.location || "—"}
                </td>
                <td className="mono px-4 py-2 text-xs text-slate-500">
                  {s.id}
                </td>
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

interface PollingSettings {
  traffic_interval_s: number;
  health_interval_s: number;
  icmp_interval_s: number;
  sync_interval_s: number;
  allowed_traffic_interval_s: number[];
  devices: number;
}

function everyLabel(seconds: number): string {
  return seconds < 60 ? `${seconds}s` : `${seconds / 60} min`;
}

// Fleet-wide collection cadence. Its own card above the poller fleet because
// it is a different thing: this is how often devices are polled, not which
// agents do the polling.
function PollingIntervalCard() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["platform", "polling"],
    queryFn: () => api<PollingSettings>("/platform/polling"),
  });
  const save = useMutation({
    mutationFn: (traffic_interval_s: number) =>
      api<PollingSettings>("/platform/polling", {
        method: "PUT",
        body: JSON.stringify({ traffic_interval_s }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["platform", "polling"] });
      // Rate lookbacks are sized from the cadence, so the charts have to be
      // told; without this they keep querying with the old window until the
      // page is reloaded.
      qc.invalidateQueries({ queryKey: ["metrics", "limits"] });
      qc.invalidateQueries({ queryKey: ["platform", "capacity"] });
    },
  });

  const s = q.data;
  return (
    <Card title="Polling interval">
      {!s ? (
        <EmptyState>Loading…</EmptyState>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-slate-500">Poll every</span>
            <Select
              aria-label="Polling interval"
              value={String(s.traffic_interval_s)}
              disabled={save.isPending}
              onChange={(e) => save.mutate(Number(e.target.value))}
            >
              {s.allowed_traffic_interval_s.map((v) => (
                <option key={v} value={v}>
                  {everyLabel(v)}
                </option>
              ))}
            </Select>
            <span className="text-sm text-slate-500">
              across {s.devices} device{s.devices === 1 ? "" : "s"}
            </span>
            {save.isPending && (
              <span className="text-xs text-slate-500">rescheduling…</span>
            )}
          </div>
          {save.error && (
            <p className="mt-2 text-sm text-red-600">
              {(save.error as Error).message}
            </p>
          )}
          <p className="mt-3 text-xs text-slate-500">
            Interface counters are read at this cadence, and device health at{" "}
            {everyLabel(s.health_interval_s)} — health never polls more often
            than traffic.{" "}
            <strong>ICMP stays at {everyLabel(s.icmp_interval_s)}</strong>{" "}
            deliberately: availability is the fastest signal that something has
            gone down, and slowing it would delay every outage alert by the same
            amount. Inventory sync runs every{" "}
            {Math.round(s.sync_interval_s / 3600)}h.
          </p>
          <p className="mt-2 text-xs text-slate-500">
            A longer interval cuts SNMP load on devices that rate-limit, and
            storage in direct proportion — polling every 5 minutes stores a
            fifth of what every minute does. It also coarsens every graph:
            spikes shorter than the interval stop being visible at all. Check{" "}
            <strong>Capacity</strong> for what the change does to disk.
          </p>
        </>
      )}
    </Card>
  );
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
      <PollingIntervalCard />
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
      <Card className="overflow-x-auto p-0">
        <table className="w-full min-w-[32rem] text-sm">
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
                  ok {p.stats?.polls_ok ?? 0} · fail{" "}
                  {p.stats?.polls_failed ?? 0} · buf{" "}
                  {p.stats?.buffer_depth ?? 0}
                </td>
                <td className="px-4 py-2 text-right">
                  {p.status === "pending" && (
                    <Button
                      variant="ghost"
                      onClick={() =>
                        action.mutate({ id: p.id, verb: "approve" })
                      }
                    >
                      Approve
                    </Button>
                  )}
                  {p.status === "active" && (
                    <Button
                      variant="ghost"
                      onClick={() =>
                        action.mutate({ id: p.id, verb: "disable" })
                      }
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

// SNMPv3 protocol choices. md5 and des are offered because legacy gear still
// requires them, and flagged because nobody should pick one by default
// (FR-COLL-01).
const authProtocols = [
  { v: "sha256", label: "SHA-256" },
  { v: "sha1", label: "SHA-1" },
  { v: "md5", label: "MD5 (deprecated)" },
];
const privProtocols = [
  { v: "aes128", label: "AES-128" },
  { v: "aes256", label: "AES-256" },
  { v: "des", label: "DES (deprecated)" },
];

function CredentialsTab() {
  const qc = useQueryClient();
  const creds = useQuery({
    queryKey: ["credentials"],
    queryFn: () => api<{ data: Credential[] }>("/credentials"),
  });

  const [kind, setKind] = useState<"snmp_v2c" | "snmp_v3">("snmp_v2c");
  const [name, setName] = useState("");
  const [community, setCommunity] = useState("");
  const [username, setUsername] = useState("");
  const [authProtocol, setAuthProtocol] = useState("sha256");
  const [authPassword, setAuthPassword] = useState("");
  // Empty means authNoPriv — the backend treats priv as optional, but demands
  // a passphrase once a protocol is chosen.
  const [privProtocol, setPrivProtocol] = useState("aes128");
  const [privPassword, setPrivPassword] = useState("");
  const [context, setContext] = useState("");

  const reset = () => {
    setName("");
    setCommunity("");
    setUsername("");
    setAuthPassword("");
    setPrivPassword("");
    setContext("");
  };

  const create = useMutation({
    mutationFn: () =>
      api("/credentials", {
        method: "POST",
        body: JSON.stringify({
          name,
          kind,
          secret:
            kind === "snmp_v2c"
              ? { community }
              : {
                  username,
                  auth_protocol: authProtocol,
                  auth_password: authPassword,
                  // Omitted entirely for authNoPriv; sending a protocol with
                  // no passphrase is rejected.
                  ...(privProtocol
                    ? {
                        priv_protocol: privProtocol,
                        priv_password: privPassword,
                      }
                    : {}),
                  ...(context ? { context } : {}),
                },
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["credentials"] });
      reset();
    },
  });

  const ready =
    !!name &&
    (kind === "snmp_v2c"
      ? !!community
      : !!username && !!authPassword && (!privProtocol || !!privPassword));

  return (
    <div className="flex flex-col gap-3">
      <Card title="Add SNMP credential">
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap gap-2">
            <Select
              value={kind}
              onChange={(e) => setKind(e.target.value as typeof kind)}
            >
              <option value="snmp_v2c">SNMPv2c</option>
              <option value="snmp_v3">SNMPv3</option>
            </Select>
            <Input
              placeholder="Name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>

          {kind === "snmp_v2c" ? (
            <Input
              type="password"
              placeholder="Community (write-only)"
              value={community}
              onChange={(e) => setCommunity(e.target.value)}
            />
          ) : (
            <div className="flex flex-col gap-3">
              <Input
                placeholder="Username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
              <div className="flex flex-wrap gap-2">
                <label className="flex min-w-40 flex-1 flex-col gap-1">
                  <span className="text-xs text-slate-500">Authentication</span>
                  <Select
                    value={authProtocol}
                    onChange={(e) => setAuthProtocol(e.target.value)}
                  >
                    {authProtocols.map((a) => (
                      <option key={a.v} value={a.v}>
                        {a.label}
                      </option>
                    ))}
                  </Select>
                </label>
                <label className="flex min-w-40 flex-1 flex-col gap-1">
                  <span className="text-xs text-slate-500">
                    Auth passphrase (write-only)
                  </span>
                  <Input
                    type="password"
                    value={authPassword}
                    onChange={(e) => setAuthPassword(e.target.value)}
                  />
                </label>
              </div>
              <div className="flex flex-wrap gap-2">
                <label className="flex min-w-40 flex-1 flex-col gap-1">
                  <span className="text-xs text-slate-500">Privacy</span>
                  <Select
                    value={privProtocol}
                    onChange={(e) => setPrivProtocol(e.target.value)}
                  >
                    {privProtocols.map((p) => (
                      <option key={p.v} value={p.v}>
                        {p.label}
                      </option>
                    ))}
                    <option value="">None (authNoPriv)</option>
                  </Select>
                </label>
                <label className="flex min-w-40 flex-1 flex-col gap-1">
                  <span className="text-xs text-slate-500">
                    Privacy passphrase
                    {privProtocol ? " (write-only)" : " — not used"}
                  </span>
                  <Input
                    type="password"
                    value={privPassword}
                    disabled={!privProtocol}
                    onChange={(e) => setPrivPassword(e.target.value)}
                  />
                </label>
              </div>
              <label className="flex flex-col gap-1">
                <span className="text-xs text-slate-500">
                  Context name — optional, only for devices that expose more
                  than one SNMP context
                </span>
                <Input
                  value={context}
                  onChange={(e) => setContext(e.target.value)}
                />
              </label>
            </div>
          )}

          {create.isError && (
            <div className="text-sm text-red-500">
              {(create.error as Error).message}
            </div>
          )}
          <div className="flex items-center gap-3">
            <Button
              disabled={!ready || create.isPending}
              onClick={() => create.mutate()}
            >
              {create.isPending ? "Saving…" : "Add credential"}
            </Button>
            <span className="text-xs text-slate-500">
              Secrets are write-only: they are encrypted on save and never
              returned by the API again (FR-CRED-01).
            </span>
          </div>
        </div>
      </Card>

      <Card className="overflow-x-auto p-0">
        <table className="w-full min-w-[32rem] text-sm">
          <tbody>
            {creds.data?.data.map((c) => (
              <tr
                key={c.id}
                className="border-b border-slate-100 dark:border-slate-800/60"
              >
                <td className="px-4 py-2 font-medium">{c.name}</td>
                <td className="px-4 py-2 text-slate-500">
                  {c.kind === "snmp_v3" ? "SNMPv3" : "SNMPv2c"}
                </td>
                <td className="px-4 py-2 text-slate-500">
                  {c.kind === "snmp_v3" ? (
                    <>
                      {c.meta.username || "—"}
                      <span className="ml-2 text-xs">
                        {c.meta.auth_protocol}
                        {c.meta.priv_protocol
                          ? ` + ${c.meta.priv_protocol}`
                          : " · authNoPriv"}
                      </span>
                    </>
                  ) : (
                    "•••"
                  )}
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
