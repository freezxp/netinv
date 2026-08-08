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
  trafficExpr,
} from "../../api/hooks";
import {
  Button,
  Card,
  EmptyState,
  SeverityPill,
  StatusBadge,
  cx,
} from "../../components/ui";
import { TimeSeries, type PromMatrix } from "../../components/TimeSeries";
import { OidBrowser } from "../inventory/OidBrowser";
import { formatBps, formatDuration, formatMs } from "../../lib/format";

const tabs = ["Interfaces", "Health", "Availability", "History", "Alerts"] as const;

export function DeviceDetailPage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const device = useDevice(id);
  const interfaces = useDeviceInterfaces(id);
  const sync = useSyncNow();
  const [tab, setTab] = useState<(typeof tabs)[number]>("Interfaces");
  const [browsing, setBrowsing] = useState(false);

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
          onClick={() => setBrowsing(true)}
          title="Walk this device and show every SNMP object it exposes"
        >
          All SNMP data
        </Button>
        <Button
          variant="ghost"
          onClick={() => sync.mutate(id)}
          disabled={sync.isPending}
        >
          {sync.isPending ? "Sync queued…" : "Sync now"}
        </Button>
      </div>
      {browsing && d && (
        <OidBrowser device={d} onClose={() => setBrowsing(false)} />
      )}

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
      {tab === "Health" && <HealthTab deviceID={id} />}
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
      ? trafficExpr(deviceID, focused.if_index)
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
                  {/* A down port that never worked doesn't alert (FR-ALR-08);
                      say so here, or its silence looks like a missed alert. */}
                  {i.oper_status === 2 && !i.ever_up && (
                    <span
                      className="ml-2 text-[10px] text-slate-500"
                      title="This port has never been seen up, so it raises no down alert. It will start alerting once it has been in service."
                    >
                      never used
                    </span>
                  )}
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
          windowHours={6}
          format={formatBps}
          label={(m) => m.dir ?? "in"}
        />
      </Card>
    </div>
  );
}

// Latest value of a range series, or undefined when the device reports none.
function latest(result: PromMatrix[] | undefined, match?: (m: Record<string, string>) => boolean) {
  const series = match ? result?.filter((r) => match(r.metric)) : result;
  const values = series?.flatMap((s) => s.values) ?? [];
  if (values.length === 0) return undefined;
  return parseFloat(values[values.length - 1][1]);
}

function HealthStat({
  label,
  value,
  unit,
  warn,
  crit,
}: {
  label: string;
  value?: number;
  unit: string;
  warn?: number;
  crit?: number;
}) {
  const tone =
    value === undefined
      ? ""
      : crit !== undefined && value >= crit
        ? "text-red-500"
        : warn !== undefined && value >= warn
          ? "text-amber-500"
          : "text-green-500";
  return (
    <Card className="flex-1 min-w-36">
      <div className="text-xs uppercase text-slate-500">{label}</div>
      <div className={`mt-1 text-2xl font-semibold ${tone}`}>
        {value === undefined ? "—" : `${value.toFixed(1)}${unit}`}
      </div>
    </Card>
  );
}

// Health tab (doc 30 §5): CPU, memory, load and per-sensor temperature.
// Sources are connector-dependent — vendor MIBs on Cisco/Juniper/Huawei,
// UCD-SNMP + LM-SENSORS on net-snmp devices such as Ubiquiti UniFi gateways.
function HealthTab({ deviceID }: { deviceID: string }) {
  const sel = `{device_id="${deviceID}"}`;
  const cpu = useQueryRange(`netinv_device_cpu_percent${sel}`, 24, 300);
  const mem = useQueryRange(`netinv_device_memory_percent${sel}`, 24, 300);
  const memBytes = useQueryRange(
    `netinv_device_memory_used_bytes${sel} or netinv_device_memory_total_bytes${sel}`,
    24,
    300,
  );
  const load = useQueryRange(`netinv_device_load_average${sel}`, 24, 300);
  const temp = useQueryRange(`netinv_sensor_temperature_celsius${sel}`, 24, 300);

  const loading = cpu.isLoading || mem.isLoading || temp.isLoading;
  const hasAny =
    (cpu.data?.length ?? 0) +
      (mem.data?.length ?? 0) +
      (load.data?.length ?? 0) +
      (temp.data?.length ?? 0) >
    0;

  if (!loading && !hasAny) {
    return (
      <Card>
        <EmptyState>
          No health metrics for this device yet. Either its connector doesn't
          expose CPU/memory/temperature, or the first health poll (every 5
          minutes by default) hasn't completed.
        </EmptyState>
      </Card>
    );
  }

  const hottest = temp.data?.length
    ? Math.max(
        ...temp.data.map((s) => parseFloat(s.values[s.values.length - 1][1])),
      )
    : undefined;
  const usedBytes = latest(memBytes.data, (m) =>
    m.__name__ === "netinv_device_memory_used_bytes",
  );
  const totalBytes = latest(memBytes.data, (m) =>
    m.__name__ === "netinv_device_memory_total_bytes",
  );

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap gap-3">
        <HealthStat label="CPU" value={latest(cpu.data)} unit="%" warn={70} crit={85} />
        <HealthStat label="Memory" value={latest(mem.data)} unit="%" warn={80} crit={90} />
        <HealthStat
          label="Load (1m)"
          value={latest(load.data, (m) => m.period === "1m")}
          unit=""
        />
        <HealthStat label="Hottest sensor" value={hottest} unit="°C" warn={70} crit={85} />
        {usedBytes !== undefined && totalBytes !== undefined && (
          <Card className="flex-1 min-w-36">
            <div className="text-xs uppercase text-slate-500">Memory used</div>
            <div className="mt-1 text-2xl font-semibold">
              {(usedBytes / 1024 ** 3).toFixed(1)} GB
            </div>
            <div className="text-xs text-slate-500">
              of {(totalBytes / 1024 ** 3).toFixed(1)} GB
            </div>
          </Card>
        )}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="CPU utilization (24h)">
          <TimeSeries
            result={cpu.data ?? []}
            windowHours={24}
            format={(v) => `${v.toFixed(0)}%`}
            label={(m) => (m.cpu ? `cpu ${m.cpu}` : "cpu")}
          />
        </Card>
        <Card title="Memory utilization (24h)">
          <TimeSeries
            result={mem.data ?? []}
            windowHours={24}
            format={(v) => `${v.toFixed(0)}%`}
            label={() => "memory"}
          />
        </Card>
        <Card title="Temperature by sensor (24h)">
          <TimeSeries
            result={temp.data ?? []}
            windowHours={24}
            format={(v) => `${v.toFixed(1)}°C`}
            label={(m) => m.sensor ?? "sensor"}
          />
        </Card>
        <Card title="Load average (24h)">
          <TimeSeries
            result={load.data ?? []}
            windowHours={24}
            format={(v) => v.toFixed(2)}
            label={(m) => m.period ?? "load"}
          />
        </Card>
      </div>
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
            windowHours={24}
          format={formatMs}
          label={(m) => m.stat ?? "rtt"}
        />
      </Card>
      <Card title="Packet loss % (24h)">
        <TimeSeries
          result={loss.data ?? []}
            windowHours={24}
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
