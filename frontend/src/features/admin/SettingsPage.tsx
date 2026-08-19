// Settings (doc 30 §10, Admin): notification channels with test-send, and
// where a copy of every collected metric is sent.
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import { Button, Card, EmptyState, Input, Select } from "../../components/ui";

interface Channel {
  id: string;
  name: string;
  kind: string;
  enabled: boolean;
  config: Record<string, unknown>;
}

export function SettingsPage() {
  const qc = useQueryClient();
  const channels = useQuery({
    queryKey: ["channels"],
    queryFn: () => api<{ data: Channel[] }>("/notification-channels"),
  });
  const [kind, setKind] = useState("slack");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [host, setHost] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [testResult, setTestResult] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () => {
      const body =
        kind === "email"
          ? {
              name,
              kind,
              config: {
                host,
                port: 25,
                from,
                to: to.split(",").map((t) => t.trim()),
              },
            }
          : kind === "webhook"
            ? { name, kind, config: { url } }
            : { name, kind, secret: { url } };
      return api("/notification-channels", {
        method: "POST",
        body: JSON.stringify(body),
      });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["channels"] });
      setName("");
    },
  });
  const test = useMutation({
    mutationFn: (id: string) =>
      api<{ result: string; detail?: string }>(
        `/notification-channels/${id}/test`,
        { method: "POST" },
      ),
    onSuccess: (r) =>
      setTestResult(r.result + (r.detail ? `: ${r.detail}` : "")),
  });
  const del = useMutation({
    mutationFn: (id: string) =>
      api(`/notification-channels/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["channels"] }),
  });

  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-3">
      <h1 className="text-xl font-semibold">Settings</h1>
      <MetricsMirrorCard />
      <h2 className="mt-2 text-lg font-semibold">Notifications</h2>
      <Card title="Add channel">
        <div className="flex flex-wrap gap-2">
          <Select value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="slack">slack</option>
            <option value="webhook">webhook</option>
            <option value="email">email</option>
          </Select>
          <Input
            placeholder="name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          {kind !== "email" && (
            <Input
              placeholder="webhook URL (write-only for slack)"
              className="w-72"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
            />
          )}
          {kind === "email" && (
            <>
              <Input
                placeholder="smtp host"
                value={host}
                onChange={(e) => setHost(e.target.value)}
              />
              <Input
                placeholder="from"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
              />
              <Input
                placeholder="to (comma separated)"
                value={to}
                onChange={(e) => setTo(e.target.value)}
              />
            </>
          )}
          <Button disabled={!name} onClick={() => create.mutate()}>
            Add
          </Button>
        </div>
        <div className="mt-2 text-xs text-slate-500">
          Default routing: critical + warning notify every enabled channel; info
          notifies none.
        </div>
      </Card>
      {testResult && (
        <div className="text-sm text-slate-500">Test result: {testResult}</div>
      )}
      <Card className="overflow-x-auto p-0">
        <table className="w-full min-w-[30rem] text-sm">
          <tbody>
            {channels.data?.data.map((c) => (
              <tr
                key={c.id}
                className="border-b border-slate-100 dark:border-slate-800/60"
              >
                <td className="px-4 py-2 font-medium">{c.name}</td>
                <td className="px-4 py-2 text-slate-500">{c.kind}</td>
                <td className="px-4 py-2 text-xs text-slate-500">
                  {c.enabled ? "enabled" : "disabled"}
                </td>
                <td className="px-4 py-2 text-right">
                  <Button variant="ghost" onClick={() => test.mutate(c.id)}>
                    Test
                  </Button>
                  <Button variant="ghost" onClick={() => del.mutate(c.id)}>
                    Delete
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {channels.data?.data.length === 0 && (
          <EmptyState>No channels configured.</EmptyState>
        )}
      </Card>
    </div>
  );
}

interface MirrorSetting {
  enabled: boolean;
  urls: string[];
}

/**
 * Where a copy of collected metrics is written, in addition to the primary
 * store — a warm standby, an off-box archive, a longer-retention instance.
 *
 * The card states the two things that are easy to assume and wrong: the copy
 * is best-effort, so it holds only what arrived while the destination was
 * reachable and nothing backfills a gap; and flow aggregates are written by a
 * separate service that has no database access, so this setting cannot reach
 * it. Saying that here is the difference between a backup someone trusts and
 * one they discover the shape of during an incident.
 */
function MetricsMirrorCard() {
  const qc = useQueryClient();
  const setting = useQuery({
    queryKey: ["settings", "metrics-mirror"],
    queryFn: () => api<MirrorSetting>("/settings/metrics-mirror"),
  });
  const [draft, setDraft] = useState<string | null>(null);
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [probe, setProbe] = useState<{ ok: boolean; detail: string } | null>(
    null,
  );

  const urls = draft ?? (setting.data?.urls ?? []).join("\n");
  const on = enabled ?? setting.data?.enabled ?? false;

  const save = useMutation({
    mutationFn: () =>
      api<MirrorSetting>("/settings/metrics-mirror", {
        method: "PUT",
        body: JSON.stringify({
          enabled: on,
          urls: urls
            .split(/[\n,]/)
            .map((u) => u.trim())
            .filter(Boolean),
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings", "metrics-mirror"] });
      setDraft(null);
      setEnabled(null);
    },
  });

  const test = useMutation({
    mutationFn: (url: string) =>
      api<{ ok: boolean; detail: string }>("/settings/metrics-mirror/test", {
        method: "POST",
        body: JSON.stringify({ url }),
      }),
    onSuccess: (r) => setProbe(r),
  });

  const first = urls.split(/[\n,]/)[0]?.trim() ?? "";

  return (
    <Card title="Metrics copy">
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={on}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Copy every collected metric to another VictoriaMetrics
      </label>
      <label className="mt-3 block text-xs text-slate-500">
        Destinations — one per line, base address only
        <textarea
          className="mono mt-1 h-20 w-full rounded border border-slate-300 bg-transparent p-2 text-xs dark:border-slate-700"
          spellCheck={false}
          value={urls}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={"http://vm-backup.example.internal:8428"}
        />
      </label>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Button disabled={save.isPending} onClick={() => save.mutate()}>
          {save.isPending ? "Saving…" : "Save"}
        </Button>
        <Button
          variant="ghost"
          disabled={!first || test.isPending}
          onClick={() => test.mutate(first)}
        >
          {test.isPending ? "Testing…" : "Test first destination"}
        </Button>
        {probe && (
          <span
            className={
              probe.ok ? "text-sm text-green-500" : "text-sm text-red-500"
            }
          >
            {probe.ok ? "Reachable — " : "Failed — "}
            {probe.detail}
          </span>
        )}
        {save.isError && (
          <span className="text-sm text-red-500">
            {(save.error as Error).message}
          </span>
        )}
        {save.isSuccess && !save.isPending && (
          <span className="text-sm text-green-500">Saved</span>
        )}
      </div>
      <p className="mt-3 text-xs text-slate-500">
        Copying is <strong>best-effort by design</strong>: a destination that is
        slow or unreachable never delays or fails collection, so the copy holds
        only what arrived while it was reachable and nothing backfills a gap.
        Watch <span className="mono">netinv_vm_mirror_failed_total</span> on the
        ingester rather than assuming silence means success. For a
        guaranteed-complete copy, put vmagent in front — it queues and replays.
      </p>
      <p className="mt-2 text-xs text-slate-500">
        This covers metrics collected from devices.{" "}
        <strong>
          Flow aggregates are written by the flow service, which has no database
          access
        </strong>
        , so it cannot read this setting — mirror those with{" "}
        <span className="mono">NETINV_VM_MIRROR_URL</span> on that container. A
        destination set in the environment also cannot be switched off here.
      </p>
    </Card>
  );
}
