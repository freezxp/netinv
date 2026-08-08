// Settings (doc 30 §10, Admin): notification channels with test-send.
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import {
  Button,
  Card,
  EmptyState,
  Input,
  Select,
} from "../../components/ui";

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
    onSuccess: (r) => setTestResult(r.result + (r.detail ? `: ${r.detail}` : "")),
  });
  const del = useMutation({
    mutationFn: (id: string) =>
      api(`/notification-channels/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["channels"] }),
  });

  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-3">
      <h1 className="text-xl font-semibold">Settings — Notifications</h1>
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
          Default routing: critical + warning notify every enabled channel;
          info notifies none.
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
