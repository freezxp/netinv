// Subnet discovery (FR-SYNC-04, doc 30 §8): define a CIDR + candidate
// credentials, sweep it, then approve or ignore each find. Nothing is ever
// auto-managed.
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import { useSites } from "../../api/hooks";
import type { Paged } from "../../api/types";
import {
  Button,
  Card,
  EmptyState,
  Input,
  Select,
  cx,
} from "../../components/ui";

interface Rule {
  id: string;
  site_id: string;
  cidr: string;
  credential_ids: string[];
  enabled: boolean;
}

interface Found {
  id: string;
  rule_id: string;
  site_id: string;
  ip: string;
  sys_name: string;
  sys_descr: string;
  sys_object_id: string;
  matched_connector_id: string;
  responding_credential_id: string;
  state: string;
  already_managed: boolean;
  seen_last_at: string;
}

interface Credential {
  id: string;
  name: string;
  kind: string;
}

export function DiscoveryTab() {
  const qc = useQueryClient();
  const sites = useSites();
  const creds = useQuery({
    queryKey: ["credentials"],
    queryFn: () => api<Paged<Credential>>("/credentials"),
  });
  const rules = useQuery({
    queryKey: ["discovery-rules"],
    queryFn: () => api<{ data: Rule[] }>("/discovery/rules"),
  });
  const found = useQuery({
    queryKey: ["discovery-found"],
    queryFn: () => api<{ data: Found[] }>("/discovery/found?state=pending"),
    refetchInterval: 10_000, // sweeps land asynchronously
  });

  const [cidr, setCidr] = useState("");
  const [siteID, setSiteID] = useState("");
  const [credIDs, setCredIDs] = useState<string[]>([]);
  const [notice, setNotice] = useState<string | null>(null);

  const createRule = useMutation({
    mutationFn: () =>
      api<Rule>("/discovery/rules", {
        method: "POST",
        body: JSON.stringify({
          site_id: siteID,
          cidr,
          credential_ids: credIDs,
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["discovery-rules"] });
      setCidr("");
    },
  });
  const run = useMutation({
    mutationFn: (ruleID: string) =>
      api<{ job_id: string }>(`/discovery/rules/${ruleID}/run`, {
        method: "POST",
      }),
    onSuccess: () =>
      setNotice("Sweep dispatched — results appear below as hosts answer."),
  });
  const del = useMutation({
    mutationFn: (ruleID: string) =>
      api(`/discovery/rules/${ruleID}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["discovery-rules"] }),
  });
  const approve = useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) =>
      api<{ device_id: string }>(`/discovery/found/${id}/approve`, {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["discovery-found"] });
      qc.invalidateQueries({ queryKey: ["devices"] });
    },
  });
  const ignore = useMutation({
    mutationFn: (id: string) =>
      api(`/discovery/found/${id}/ignore`, { method: "POST" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["discovery-found"] }),
  });

  const toggleCred = (id: string) =>
    setCredIDs((c) => (c.includes(id) ? c.filter((x) => x !== id) : [...c, id]));

  return (
    <div className="flex flex-col gap-3">
      <Card title="Scan a subnet">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            placeholder="CIDR, e.g. 10.0.30.0/24"
            className="w-52"
            value={cidr}
            onChange={(e) => setCidr(e.target.value)}
          />
          <Select value={siteID} onChange={(e) => setSiteID(e.target.value)}>
            <option value="">Site…</option>
            {sites.data?.data.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </Select>
          <Button
            disabled={!cidr || !siteID || credIDs.length === 0 || createRule.isPending}
            onClick={() => createRule.mutate()}
          >
            Add scan
          </Button>
        </div>
        <div className="mt-2 flex flex-wrap gap-1.5">
          <span className="text-xs text-slate-500">Try credentials:</span>
          {creds.data?.data.map((c) => (
            <button
              key={c.id}
              onClick={() => toggleCred(c.id)}
              className={cx(
                "rounded px-2 py-0.5 text-xs",
                credIDs.includes(c.id)
                  ? "bg-sky-600/20 text-sky-500"
                  : "bg-slate-200 text-slate-500 dark:bg-slate-800",
              )}
            >
              {c.name}
            </button>
          ))}
          {creds.data?.data.length === 0 && (
            <span className="text-xs text-slate-500">
              Add a credential first (Credentials tab).
            </span>
          )}
        </div>
        {createRule.isError && (
          <div className="mt-2 text-sm text-red-500">
            {(createRule.error as Error).message}
          </div>
        )}
        <div className="mt-2 text-xs text-slate-500">
          Sweeps probe SNMP on each address (max /20 per scan, 48 at a time).
          Discovered devices are never managed automatically — you approve each.
        </div>
      </Card>

      {rules.data?.data.length ? (
        <Card className="p-0">
          <table className="w-full text-sm">
            <tbody>
              {rules.data.data.map((r) => (
                <tr
                  key={r.id}
                  className="border-b border-slate-100 dark:border-slate-800/60"
                >
                  <td className="mono px-4 py-2 font-medium">{r.cidr}</td>
                  <td className="px-4 py-2 text-slate-500">
                    {sites.data?.data.find((s) => s.id === r.site_id)?.name ??
                      r.site_id}
                  </td>
                  <td className="px-4 py-2 text-xs text-slate-500">
                    {r.credential_ids.length} credential
                    {r.credential_ids.length === 1 ? "" : "s"}
                  </td>
                  <td className="px-4 py-2 text-right">
                    <Button
                      variant="ghost"
                      disabled={run.isPending}
                      onClick={() => run.mutate(r.id)}
                    >
                      {run.isPending ? "Scanning…" : "Scan now"}
                    </Button>
                    <Button variant="ghost" onClick={() => del.mutate(r.id)}>
                      Delete
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      ) : null}

      {notice && <div className="text-sm text-sky-500">{notice}</div>}

      <Card title={`Discovered — pending approval (${found.data?.data.length ?? 0})`} className="p-0">
        <div className="p-4">
          {found.data?.data.length === 0 && (
            <EmptyState>
              Nothing pending. Add a scan above and press “Scan now”.
            </EmptyState>
          )}
          <div className="flex flex-col divide-y divide-slate-100 dark:divide-slate-800">
            {found.data?.data.map((f) => (
              <FoundRow
                key={f.id}
                found={f}
                onApprove={(name) => approve.mutate({ id: f.id, name })}
                onIgnore={() => ignore.mutate(f.id)}
                busy={approve.isPending || ignore.isPending}
              />
            ))}
          </div>
          {approve.isError && (
            <div className="mt-2 text-sm text-red-500">
              {(approve.error as Error).message}
            </div>
          )}
        </div>
      </Card>
    </div>
  );
}

function FoundRow({
  found,
  onApprove,
  onIgnore,
  busy,
}: {
  found: Found;
  onApprove: (name: string) => void;
  onIgnore: () => void;
  busy: boolean;
}) {
  const [name, setName] = useState(found.sys_name || found.ip);
  return (
    <div className="flex flex-wrap items-center gap-2 py-2">
      <span className="mono w-32 font-medium">{found.ip}</span>
      <span className="min-w-0 flex-1 truncate text-sm text-slate-500">
        {found.sys_descr || "(no description)"}
      </span>
      {found.matched_connector_id && (
        <span className="rounded bg-sky-600/10 px-1.5 py-0.5 text-xs text-sky-500">
          {found.matched_connector_id}
        </span>
      )}
      {found.already_managed ? (
        <span className="text-xs text-slate-500">already managed</span>
      ) : (
        <>
          <Input
            className="w-40"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Button disabled={busy} onClick={() => onApprove(name)}>
            Add
          </Button>
        </>
      )}
      <Button variant="ghost" disabled={busy} onClick={onIgnore}>
        Ignore
      </Button>
    </div>
  );
}
