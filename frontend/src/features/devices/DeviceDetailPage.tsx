// Device detail (doc 30 §5): identity header, interface table with focus
// graph (the alert deep-link target — PRD G3), availability, history, alerts.
import { useMemo, useState } from "react";
import { useParams, useSearchParams } from "react-router-dom";
import {
  useDevice,
  useDeviceAlerts,
  useDeviceHistory,
  useDeviceInterfaces,
  useQueryRange,
  useSyncNow,
} from "../../api/hooks";
import {
  Button,
  Card,
  EmptyState,
  SeverityPill,
  StatusBadge,
  cx,
} from "../../components/ui";
import { TimeSeries } from "../../components/TimeSeries";
import { formatBps, formatDuration, formatMs } from "../../lib/format";

const tabs = ["Interfaces", "Availability", "History", "Alerts"] as const;

export function DeviceDetailPage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const device = useDevice(id);
  const interfaces = useDeviceInterfaces(id);
  const sync = useSyncNow();
  const [tab, setTab] = useState<(typeof tabs)[number]>("Interfaces");

  const focusIf = params.get("if") ?? "";
  const d = device.data;

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        {d && <StatusBadge status={d.status} />}
        <h1 className="text-xl font-semibold">{d?.name ?? "…"}</h1>
        <span className="mono text-sm text-slate-500">{d?.mgmt_ip}</span>
        {d?.vendor && (
          <span className="rounded bg-slate-200 px-2 py-0.5 text-xs dark:bg-slate-800">
            {d.vendor} {d.model}
          </span>
        )}
        {d?.os_version && (
          <span className="rounded bg-slate-200 px-2 py-0.5 text-xs dark:bg-slate-800">
            {d.os_version}
          </span>
        )}
        <div className="flex-1" />
        <Button
          variant="ghost"
          onClick={() => sync.mutate(id)}
          disabled={sync.isPending}
        >
          {sync.isPending ? "Sync queued…" : "Sync now"}
        </Button>
      </div>

      {d?.sys_name && (
        <div className="text-sm text-slate-500">
          sysName <span className="mono">{d.sys_name}</span>
          {d.serial_number && (
            <>
              {" · "}serial <span className="mono">{d.serial_number}</span>
            </>
          )}
        </div>
      )}

      <div className="flex gap-1 border-b border-slate-200 dark:border-slate-800">
        {tabs.map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={cx(
              "px-3 py-2 text-sm",
              t === tab
                ? "border-b-2 border-sky-500 font-medium text-sky-500"
                : "text-slate-500 hover:text-slate-700 dark:hover:text-slate-300",
            )}
          >
            {t}
          </button>
        ))}
      </div>

      {tab === "Interfaces" && (
        <InterfacesTab
          deviceID={id}
          rows={interfaces.data?.data ?? []}
          focusIf={focusIf}
          onFocus={(ifIdx) => {
            const p = new URLSearchParams(params);
            p.set("if", ifIdx);
            setParams(p, { replace: true });
          }}
        />
      )}
      {tab === "Availability" && <AvailabilityTab deviceID={id} />}
      {tab === "History" && <HistoryTab deviceID={id} />}
      {tab === "Alerts" && <AlertsTab deviceID={id} />}
    </div>
  );
}

function statusWord(n: number) {
  return n === 1 ? "up" : n === 2 ? "down" : "—";
}

function InterfacesTab({
  deviceID,
  rows,
  focusIf,
  onFocus,
}: {
  deviceID: string;
  rows: ReturnType<typeof useDeviceInterfaces>["data"] extends
    | { data: infer T }
    | undefined
    ? T
    : never;
  focusIf: string;
  onFocus: (ifIdx: string) => void;
}) {
  const focused = useMemo(
    () => rows.find((r) => String(r.if_index) === focusIf) ?? rows[0],
    [rows, focusIf],
  );
  const traffic = useQueryRange(
    focused
      ? `rate(netinv_if_in_octets_total{device_id="${deviceID}",if_index="${focused.if_index}"}[5m]) * 8 or rate(netinv_if_out_octets_total{device_id="${deviceID}",if_index="${focused.if_index}"}[5m]) * 8`
      : `vector(0)`,
    6,
    120,
  );
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card className="overflow-x-auto p-0">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-slate-200 text-left text-xs uppercase text-slate-500 dark:border-slate-800">
              {["If", "Name", "Alias", "Speed", "Oper", "Admin", "State"].map((h) => (
                <th key={h} className="px-3 py-2 font-medium">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((i) => (
              <tr
                key={i.id}
                onClick={() => onFocus(String(i.if_index))}
                className={cx(
                  "cursor-pointer border-b border-slate-100 dark:border-slate-800/60",
                  focused?.id === i.id
                    ? "bg-sky-600/10"
                    : "hover:bg-slate-50 dark:hover:bg-slate-800/40",
                )}
              >
                <td className="px-3 py-1.5">{i.if_index}</td>
                <td className="mono px-3 py-1.5">{i.name}</td>
                <td className="px-3 py-1.5 text-slate-500">{i.alias || "—"}</td>
                <td className="px-3 py-1.5">
                  {i.speed_bps ? formatBps(i.speed_bps) : "—"}
                </td>
                <td className="px-3 py-1.5">
                  <StatusBadge
                    status={i.oper_status === 1 ? "up" : i.oper_status === 2 ? "critical" : "unreachable"}
                  />
                </td>
                <td className="px-3 py-1.5">{statusWord(i.admin_status)}</td>
                <td className="px-3 py-1.5 text-slate-500">{i.state}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {rows.length === 0 && (
          <EmptyState>No interfaces yet — waiting for first sync.</EmptyState>
        )}
      </Card>
      <Card
        title={
          focused
            ? `Traffic — ${focused.name || `if ${focused.if_index}`} (6h)`
            : "Traffic"
        }
      >
        <TimeSeries
          result={traffic.data ?? []}
          format={formatBps}
          label={(m) => (m.__name__?.includes("out") ? "out" : "in")}
        />
      </Card>
    </div>
  );
}

function AvailabilityTab({ deviceID }: { deviceID: string }) {
  const rtt = useQueryRange(
    `netinv_icmp_rtt_seconds{device_id="${deviceID}"}`,
    24,
    300,
  );
  const loss = useQueryRange(
    `netinv_icmp_loss_ratio{device_id="${deviceID}"} * 100`,
    24,
    300,
  );
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card title="RTT min/avg/max (24h)">
        <TimeSeries
          result={rtt.data ?? []}
          format={formatMs}
          label={(m) => m.stat ?? "rtt"}
        />
      </Card>
      <Card title="Packet loss % (24h)">
        <TimeSeries
          result={loss.data ?? []}
          format={(v) => `${v.toFixed(1)}%`}
          label={() => "loss"}
        />
      </Card>
    </div>
  );
}

function HistoryTab({ deviceID }: { deviceID: string }) {
  const history = useDeviceHistory(deviceID);
  return (
    <Card className="p-0">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-slate-200 text-left text-xs uppercase text-slate-500 dark:border-slate-800">
            {["When", "Object", "Field", "Change"].map((h) => (
              <th key={h} className="px-4 py-2 font-medium">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {history.data?.data.map((h, i) => (
            <tr key={i} className="border-b border-slate-100 dark:border-slate-800/60">
              <td className="px-4 py-1.5 text-slate-500">
                {new Date(h.detected_at).toLocaleString()}
              </td>
              <td className="px-4 py-1.5">{h.object_kind}</td>
              <td className="px-4 py-1.5">{h.field}</td>
              <td className="px-4 py-1.5">
                {h.change_kind === "created" ? (
                  <span className="text-green-500">+ {h.new_value}</span>
                ) : h.change_kind === "removed" ? (
                  <span className="text-red-500">− {h.old_value}</span>
                ) : (
                  <span>
                    <span className="text-slate-500 line-through">
                      {h.old_value || "∅"}
                    </span>{" "}
                    → {h.new_value}
                  </span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {history.data?.data.length === 0 && (
        <EmptyState>No recorded changes yet.</EmptyState>
      )}
    </Card>
  );
}

function AlertsTab({ deviceID }: { deviceID: string }) {
  const alerts = useDeviceAlerts(deviceID);
  return (
    <Card className="p-0">
      <div className="flex flex-col divide-y divide-slate-100 p-4 dark:divide-slate-800">
        {alerts.data?.data.length === 0 && (
          <EmptyState>No active alerts for this device.</EmptyState>
        )}
        {alerts.data?.data.map((a) => (
          <div key={a.id} className="flex items-center gap-3 py-2">
            <SeverityPill severity={a.severity} />
            <span className="flex-1">{a.rule.name}</span>
            <span className="text-sm text-slate-500">
              {a.state} · {formatDuration(a.duration_s)}
            </span>
          </div>
        ))}
      </div>
    </Card>
  );
}
