// Alerts page (doc 30 §6): active list with ack, silences, rule management.
import { useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import { useAckAlert, useAlerts } from "../../api/hooks";
import {
  Button,
  Card,
  EmptyState,
  Input,
  SeverityPill,
  cx,
} from "../../components/ui";
import { formatDuration } from "../../lib/format";

const tabs = ["Active", "Silences", "Rules"] as const;

export function AlertsPage() {
  const [tab, setTab] = useState<(typeof tabs)[number]>("Active");
  return (
    <div className="mx-auto max-w-5xl">
      <h1 className="mb-3 text-xl font-semibold">Alerts</h1>
      <div className="mb-4 flex gap-1 border-b border-slate-200 dark:border-slate-800">
        {tabs.map((t) => (
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
      {tab === "Active" && <ActiveTab />}
      {tab === "Silences" && <SilencesTab />}
      {tab === "Rules" && <RulesTab />}
    </div>
  );
}

function ActiveTab() {
  const alerts = useAlerts();
  const ack = useAckAlert();
  const [comment, setComment] = useState("");
  return (
    <Card className="p-0">
      <div className="flex flex-col divide-y divide-slate-100 p-4 dark:divide-slate-800">
        {alerts.data?.data.length === 0 && (
          <EmptyState>No active alerts.</EmptyState>
        )}
        {alerts.data?.data.map((a) => (
          <div key={a.id} className="flex items-center gap-3 py-2.5">
            <SeverityPill severity={a.severity} />
            <div className="flex-1">
              <div className="font-medium">{a.rule.name}</div>
              <div className="text-sm text-slate-500">
                {a.labels.device}
                {a.labels.if_index && ` · if ${a.labels.if_index}`} ·{" "}
                {formatDuration(a.duration_s)} · {a.state}
                {a.acked && ` — "${a.acked.comment}"`}
              </div>
            </div>
            {a.state !== "acknowledged" && (
              <>
                <Input
                  placeholder="comment"
                  className="w-40"
                  onChange={(e) => setComment(e.target.value)}
                />
                <Button
                  variant="ghost"
                  onClick={() => ack.mutate({ id: a.id, comment })}
                >
                  Ack
                </Button>
              </>
            )}
            {a.device_id && (
              <Link
                className="text-sm text-sky-500 hover:underline"
                to={`/devices/${a.device_id}${a.labels.if_index ? `?if=${a.labels.if_index}` : ""}`}
              >
                Graph →
              </Link>
            )}
          </div>
        ))}
      </div>
    </Card>
  );
}

interface Silence {
  id: string;
  scope: Record<string, string>;
  reason: string;
  ends_at: string;
  active: boolean;
}

function SilencesTab() {
  const qc = useQueryClient();
  const silences = useQuery({
    queryKey: ["silences"],
    queryFn: () => api<{ data: Silence[] }>("/silences"),
  });
  const [device, setDevice] = useState("");
  const [reason, setReason] = useState("");
  const create = useMutation({
    mutationFn: () =>
      api("/silences", {
        method: "POST",
        body: JSON.stringify({
          scope: { device },
          reason,
          duration_s: 3600,
        }),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["silences"] }),
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api(`/silences/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["silences"] }),
  });
  return (
    <div className="flex flex-col gap-3">
      <Card title="Silence a device for 1 hour">
        <div className="flex gap-2">
          <Input
            placeholder="device label (e.g. west-sw-1)"
            value={device}
            onChange={(e) => setDevice(e.target.value)}
          />
          <Input
            placeholder="reason (required)"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
          <Button
            disabled={!device || !reason || create.isPending}
            onClick={() => create.mutate()}
          >
            Silence
          </Button>
        </div>
      </Card>
      <Card className="p-0">
        <div className="flex flex-col divide-y divide-slate-100 p-4 dark:divide-slate-800">
          {silences.data?.data.length === 0 && (
            <EmptyState>No silences.</EmptyState>
          )}
          {silences.data?.data.map((s) => (
            <div key={s.id} className="flex items-center gap-3 py-2">
              <span
                className={cx(
                  "rounded px-1.5 text-xs",
                  s.active
                    ? "bg-amber-500/20 text-amber-500"
                    : "bg-slate-500/20 text-slate-500",
                )}
              >
                {s.active ? "active" : "ended"}
              </span>
              <span className="mono flex-1 text-sm">
                {JSON.stringify(s.scope)}
              </span>
              <span className="text-sm text-slate-500">{s.reason}</span>
              {s.active && (
                <Button variant="ghost" onClick={() => revoke.mutate(s.id)}>
                  Revoke
                </Button>
              )}
            </div>
          ))}
        </div>
      </Card>
    </div>
  );
}

interface Rule {
  id: string;
  name: string;
  kind: string;
  severity: string;
  expr: string;
  enabled: boolean;
  is_builtin: boolean;
}

function RulesTab() {
  const qc = useQueryClient();
  const rules = useQuery({
    queryKey: ["alert-rules"],
    queryFn: () => api<{ data: Rule[] }>("/alert-rules"),
  });
  const toggle = useMutation({
    mutationFn: ({ id, enable }: { id: string; enable: boolean }) =>
      api(`/alert-rules/${id}/${enable ? "enable" : "disable"}`, {
        method: "POST",
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alert-rules"] }),
  });
  return (
    <Card className="p-0">
      <table className="w-full text-sm">
        <tbody>
          {rules.data?.data.map((r) => (
            <tr
              key={r.id}
              className="border-b border-slate-100 dark:border-slate-800/60"
            >
              <td className="px-4 py-2">
                <SeverityPill severity={r.severity} />
              </td>
              <td className="px-4 py-2 font-medium">
                {r.name}
                {r.is_builtin && (
                  <span className="ml-2 text-xs text-slate-500">builtin</span>
                )}
              </td>
              <td className="mono max-w-md truncate px-4 py-2 text-xs text-slate-500">
                {r.expr || r.kind}
              </td>
              <td className="px-4 py-2 text-right">
                <Button
                  variant="ghost"
                  onClick={() => toggle.mutate({ id: r.id, enable: !r.enabled })}
                >
                  {r.enabled ? "Disable" : "Enable"}
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}
