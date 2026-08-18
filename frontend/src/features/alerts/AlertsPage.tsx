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
  Select,
  SeverityPill,
  cx,
} from "../../components/ui";
import { hasPermissionRole, useAuthStore } from "../auth/store";
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
    <Card className="overflow-x-auto p-0">
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
                {/* Joined rather than interleaved with separators: not every
                    alert is device-scoped. The flow rules (doc 34 §5.1) carry an
                    `exporter` label or, for the fleet-wide one, no labels at all,
                    and hardcoding " · " between fields left a dangling separator
                    and hid which exporter had stopped — the one thing that alert
                    exists to say. */}
                {[
                  a.labels.device || a.labels.exporter,
                  a.labels.if_index && `if ${a.labels.if_index}`,
                  formatDuration(a.duration_s),
                  a.state,
                ]
                  .filter(Boolean)
                  .join(" · ")}
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
      <Card className="overflow-x-auto p-0">
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
  condition?: Record<string, unknown>;
  annotations?: Record<string, string>;
  enabled: boolean;
  is_builtin: boolean;
  /** Live alerts from this rule — shown before disabling or deleting it. */
  firing: number;
}

function RulesTab() {
  const qc = useQueryClient();
  const canEdit = hasPermissionRole(useAuthStore((s) => s.user), "operator");
  const [editing, setEditing] = useState<Rule | "new" | null>(null);
  const [deleting, setDeleting] = useState<Rule | null>(null);
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
    <div className="flex flex-col gap-3">
      {canEdit && (
        <div className="flex justify-end">
          <Button onClick={() => setEditing("new")}>New rule</Button>
        </div>
      )}
      <Card className="overflow-x-auto p-0">
        <table className="w-full min-w-[34rem] text-sm">
          <tbody>
            {rules.data?.data.map((r) => (
              <tr
                key={r.id}
                className={cx(
                  "border-b border-slate-100 dark:border-slate-800/60",
                  !r.enabled && "opacity-60",
                )}
              >
                <td className="px-4 py-2">
                  <SeverityPill severity={r.severity} />
                </td>
                <td className="px-4 py-2 font-medium">
                  {r.name}
                  {r.is_builtin && (
                    <span className="ml-2 text-xs text-slate-500">builtin</span>
                  )}
                  {!r.enabled && (
                    <span className="ml-2 text-xs text-amber-500">disabled</span>
                  )}
                </td>
                <td className="mono max-w-md truncate px-4 py-2 text-xs text-slate-500">
                  {r.expr || r.kind}
                </td>
                <td className="px-4 py-2 text-xs text-slate-500">
                  {r.firing > 0 ? `${r.firing} firing` : ""}
                </td>
                <td className="whitespace-nowrap px-4 py-2 text-right">
                  {canEdit && (
                    <Button variant="ghost" onClick={() => setEditing(r)}>
                      Edit
                    </Button>
                  )}
                  <Button
                    variant="ghost"
                    disabled={!canEdit || toggle.isPending}
                    onClick={() => toggle.mutate({ id: r.id, enable: !r.enabled })}
                  >
                    {r.enabled ? "Disable" : "Enable"}
                  </Button>
                  {/* Built-ins are tunable but not removable — nothing short of
                      a re-migration would bring one back. */}
                  {canEdit && !r.is_builtin && (
                    <Button variant="danger" onClick={() => setDeleting(r)}>
                      Delete
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {rules.data?.data.length === 0 && (
          <EmptyState>No alert rules yet.</EmptyState>
        )}
      </Card>
      {editing && (
        <RuleFormModal
          rule={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <ConfirmDeleteRule rule={deleting} onClose={() => setDeleting(null)} />
      )}
    </div>
  );
}

const severities = ["critical", "warning", "info"];

function RuleFormModal({
  rule,
  onClose,
}: {
  rule?: Rule;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState(rule?.name ?? "");
  const [severity, setSeverity] = useState(rule?.severity ?? "warning");
  const [expr, setExpr] = useState(rule?.expr ?? "");
  const [summary, setSummary] = useState(rule?.annotations?.summary ?? "");

  const save = useMutation({
    mutationFn: () =>
      api<Rule>(rule ? `/alert-rules/${rule.id}` : "/alert-rules", {
        method: rule ? "PATCH" : "POST",
        body: JSON.stringify({
          name,
          severity,
          expr,
          annotations: summary ? { summary } : {},
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["alert-rules"] });
      onClose();
    },
  });

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50 p-4 whitespace-normal">
      <Card
        title={rule ? `Edit ${rule.name}` : "New alert rule"}
        className="w-full max-w-[34rem]"
      >
        <div className="flex flex-col gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-xs text-slate-500">Name</span>
            <Input
              value={name}
              autoFocus
              onChange={(e) => setName(e.target.value)}
              placeholder="Temperature above 75C"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-slate-500">Severity</span>
            <Select
              value={severity}
              onChange={(e) => setSeverity(e.target.value)}
            >
              {severities.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </Select>
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-slate-500">
              Expression (MetricsQL) — checked against the metrics backend
              before saving, so a typo is refused here rather than silently
              never firing
            </span>
            <textarea
              value={expr}
              onChange={(e) => setExpr(e.target.value)}
              rows={3}
              spellCheck={false}
              placeholder="max_over_time(netinv_sensor_temperature_celsius[10m]) > 75"
              className="mono rounded-md border border-slate-300 bg-white px-3 py-1.5 text-xs outline-none focus:border-sky-500 focus:ring-1 focus:ring-sky-500 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-slate-500">
              Summary — {"{{device}}"} and {"{{if_name}}"} are filled in per alert
            </span>
            <Input
              value={summary}
              onChange={(e) => setSummary(e.target.value)}
              placeholder="{{device}} sensor above 75C"
            />
          </label>
          {rule?.is_builtin && (
            <p className="text-xs text-slate-500">
              This is a built-in rule. Tuning it is fine — the change sticks
              across restarts — but it cannot be deleted, only disabled.
            </p>
          )}
          {save.isError && (
            <div className="text-sm text-red-500">
              {(save.error as Error).message}
            </div>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button
              disabled={!name || !expr || save.isPending}
              onClick={() => save.mutate()}
            >
              {save.isPending ? "Saving…" : rule ? "Save" : "Create rule"}
            </Button>
          </div>
        </div>
      </Card>
    </div>
  );
}

function ConfirmDeleteRule({
  rule,
  onClose,
}: {
  rule: Rule;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [typed, setTyped] = useState("");
  const del = useMutation({
    mutationFn: () => api<void>(`/alert-rules/${rule.id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["alert-rules"] });
      qc.invalidateQueries({ queryKey: ["alerts"] });
      onClose();
    },
  });
  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50 p-4 whitespace-normal">
      <Card title="Delete alert rule" className="w-full max-w-[26rem]">
        <p className="text-sm">
          This removes <span className="font-medium">{rule.name}</span> and the
          alert history recorded against it. It cannot be undone.
        </p>
        {rule.firing > 0 && (
          <p className="mt-2 text-sm text-amber-500">
            {rule.firing} alert{rule.firing === 1 ? "" : "s"} from this rule
            {rule.firing === 1 ? " is" : " are"} live right now and will
            disappear. Disable the rule instead if you only want it to stop
            firing.
          </p>
        )}
        <p className="mt-2 text-xs text-slate-500">
          Nothing stops being monitored by anything else; only this rule's own
          evaluations end. Audit records of the rule and its deletion are kept.
        </p>
        <label className="mt-3 block text-xs text-slate-500">
          Type the rule name to confirm:
          <Input
            className="mt-1 w-full"
            value={typed}
            autoFocus
            onChange={(e) => setTyped(e.target.value)}
            placeholder={rule.name}
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
            disabled={typed !== rule.name || del.isPending}
            onClick={() => del.mutate()}
          >
            {del.isPending ? "Deleting…" : "Delete permanently"}
          </Button>
        </div>
      </Card>
    </div>
  );
}
