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
  useMetricsLimits,
  useSyncNow,
  trafficExpr,
  seriesExpr,
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
import { RangePicker } from "../../components/RangePicker";
import { rateWindow, useTimeRange } from "../../api/timerange";
import { OidBrowser } from "../inventory/OidBrowser";
import { formatBps, formatDuration, formatMs } from "../../lib/format";

const baseTabs = [
  "Interfaces",
  "Health",
  "Availability",
  "History",
  "Alerts",
] as const;
type Tab = (typeof baseTabs)[number] | "Wireless";

// Tabs whose content is time-series. History and Alerts are tables over their
// own lifecycle timestamps and are not affected by the range selector.
const GRAPH_TABS = new Set<Tab>([
  "Interfaces",
  "Health",
  "Availability",
  "Wireless",
]);

export function DeviceDetailPage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const device = useDevice(id);
  const interfaces = useDeviceInterfaces(id);
  const sync = useSyncNow();
  const [tab, setTab] = useState<Tab>("Interfaces");
  const [browsing, setBrowsing] = useState(false);

  // Wireless is only meaningful for a controller/AP, so the tab appears only
  // when the device actually reports clients — no empty tab on a router.
  const wirelessProbe = useQueryRange(
    `netinv_wireless_client_count{device_id="${id}"}`,
    1,
    300,
  );
  const tabs: readonly Tab[] =
    (wirelessProbe.data?.length ?? 0)
      ? ([...baseTabs.slice(0, 2), "Wireless", ...baseTabs.slice(2)] as Tab[])
      : baseTabs;

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

      <div className="flex items-end gap-1 border-b border-slate-200 dark:border-slate-800">
        <div className="flex flex-1 gap-1 overflow-x-auto">
          {tabs.map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={cx(
                "shrink-0 px-3 py-2 text-sm",
                t === tab
                  ? "border-b-2 border-sky-500 font-medium text-sky-500"
                  : "text-slate-500 hover:text-slate-700 dark:hover:text-slate-300",
              )}
            >
              {t}
            </button>
          ))}
        </div>
        {/* Only on tabs that draw graphs. Showing it over the History table
            would imply it filters those rows, which it does not. */}
        {GRAPH_TABS.has(tab) && (
          <RangePicker className="mb-1.5 ml-2" ariaLabel="Graph time range" />
        )}
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
      {tab === "Wireless" && <WirelessTab deviceID={id} />}
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
    { data: infer T } | undefined
    ? T
    : never;
  focusIf: string;
  onFocus: (ifIdx: string) => void;
}) {
  const focused = useMemo(
    () => rows.find((r) => String(r.if_index) === focusIf) ?? rows[0],
    [rows, focusIf],
  );
  const range = useTimeRange();
  const pollS = useMetricsLimits().data?.poll_interval_s ?? 0;
  const traffic = useQueryRange(
    focused
      ? trafficExpr(deviceID, focused.if_index, rateWindow(range.stepS, pollS))
      : `vector(0)`,
    range.hours,
    range.stepS,
  );
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card className="overflow-x-auto p-0">
        <table className="w-full min-w-[36rem] text-sm">
          <thead>
            <tr className="border-b border-slate-200 text-left text-xs uppercase text-slate-500 dark:border-slate-800">
              {["If", "Name", "Alias", "Speed", "Oper", "Admin", "State"].map(
                (h) => (
                  <th key={h} className="px-3 py-2 font-medium">
                    {h}
                  </th>
                ),
              )}
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
                    status={
                      i.oper_status === 1
                        ? "up"
                        : i.oper_status === 2
                          ? "critical"
                          : "unreachable"
                    }
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
          windowHours={range.hours}
          format={formatBps}
          label={(m) => m.dir ?? "in"}
        />
      </Card>
    </div>
  );
}

// Latest value of a range series, or undefined when the device reports none.
function latest(
  result: PromMatrix[] | undefined,
  match?: (m: Record<string, string>) => boolean,
) {
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

// Wireless tab: what a controller reports about its radio estate. Ruckus
// Unleashed is the case in hand — it exposes client and AP counts but no
// CPU/memory/temperature at all, so this is the only health signal it has.
function WirelessTab({ deviceID }: { deviceID: string }) {
  const range = useTimeRange();
  const sel = `{device_id="${deviceID}"}`;
  const clients = useQueryRange(
    `netinv_wireless_client_count${sel}`,
    range.hours,
    range.stepS,
  );
  const aps = useQueryRange(
    seriesExpr(sel, [
      ["up", "netinv_wireless_ap_up_count"],
      ["total", "netinv_wireless_ap_total"],
    ]),
    range.hours,
    range.stepS,
  );
  const now = latest(clients.data);
  const up = latest(aps.data, (m) => m.series === "up");
  const total = latest(aps.data, (m) => m.series === "total");
  const apsDown = up !== undefined && total !== undefined && up < total;

  const peak = clients.data?.length
    ? Math.max(
        ...clients.data.flatMap((s) => s.values.map((v) => parseFloat(v[1]))),
      )
    : undefined;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap gap-3">
        <Card className="min-w-36 flex-1">
          <div className="text-xs uppercase text-slate-500">
            Connected clients
          </div>
          <div className="mt-1 text-2xl font-semibold text-sky-500">
            {now === undefined ? "—" : now.toFixed(0)}
          </div>
        </Card>
        <Card className="min-w-36 flex-1">
          <div className="text-xs uppercase text-slate-500">Peak (24h)</div>
          <div className="mt-1 text-2xl font-semibold">
            {peak === undefined ? "—" : peak.toFixed(0)}
          </div>
        </Card>
        <Card className="min-w-36 flex-1">
          <div className="text-xs uppercase text-slate-500">Access points</div>
          <div
            className={cx(
              "mt-1 text-2xl font-semibold",
              apsDown ? "text-amber-500" : "text-green-500",
            )}
          >
            {up === undefined || total === undefined
              ? "—"
              : `${up.toFixed(0)} / ${total.toFixed(0)}`}
          </div>
          {apsDown && (
            <div className="mt-1 text-xs text-amber-500">
              {(total! - up!).toFixed(0)} not reporting
            </div>
          )}
        </Card>
      </div>
      <Card title={`Connected clients (${range.short})`}>
        <TimeSeries
          result={clients.data ?? []}
          windowHours={range.hours}
          format={(v) => v.toFixed(0)}
          label={() => "clients"}
        />
      </Card>
      <Card title={`Access points up (${range.short})`}>
        <TimeSeries
          result={aps.data ?? []}
          windowHours={range.hours}
          format={(v) => v.toFixed(0)}
          label={(m) => m.series ?? "up"}
        />
      </Card>
    </div>
  );
}

// Health tab (doc 30 §5): CPU, memory, load and per-sensor temperature.
// Sources are connector-dependent — vendor MIBs on Cisco/Juniper/Huawei,
// UCD-SNMP + LM-SENSORS on net-snmp devices such as Ubiquiti UniFi gateways.
function HealthTab({ deviceID }: { deviceID: string }) {
  const range = useTimeRange();
  const sel = `{device_id="${deviceID}"}`;
  const cpu = useQueryRange(
    `netinv_device_cpu_percent${sel}`,
    range.hours,
    range.stepS,
  );
  const mem = useQueryRange(
    `netinv_device_memory_percent${sel}`,
    range.hours,
    range.stepS,
  );
  const memBytes = useQueryRange(
    seriesExpr(sel, [
      ["used", "netinv_device_memory_used_bytes"],
      ["total", "netinv_device_memory_total_bytes"],
    ]),
    range.hours,
    range.stepS,
  );
  const load = useQueryRange(
    `netinv_device_load_average${sel}`,
    range.hours,
    range.stepS,
  );
  const temp = useQueryRange(
    `netinv_sensor_temperature_celsius${sel}`,
    range.hours,
    range.stepS,
  );

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
  const usedBytes = latest(memBytes.data, (m) => m.series === "used");
  const totalBytes = latest(memBytes.data, (m) => m.series === "total");

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap gap-3">
        <HealthStat
          label="CPU"
          value={latest(cpu.data)}
          unit="%"
          warn={70}
          crit={85}
        />
        <HealthStat
          label="Memory"
          value={latest(mem.data)}
          unit="%"
          warn={80}
          crit={90}
        />
        <HealthStat
          label="Load (1m)"
          value={latest(load.data, (m) => m.period === "1m")}
          unit=""
        />
        <HealthStat
          label="Hottest sensor"
          value={hottest}
          unit="°C"
          warn={70}
          crit={85}
        />
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
        <Card title={`CPU utilization (${range.short})`}>
          <TimeSeries
            result={cpu.data ?? []}
            windowHours={range.hours}
            format={(v) => `${v.toFixed(0)}%`}
            label={(m) => (m.cpu ? `cpu ${m.cpu}` : "cpu")}
          />
        </Card>
        <Card title={`Memory utilization (${range.short})`}>
          <TimeSeries
            result={mem.data ?? []}
            windowHours={range.hours}
            format={(v) => `${v.toFixed(0)}%`}
            label={() => "memory"}
          />
        </Card>
        <Card title={`Temperature by sensor (${range.short})`}>
          <TimeSeries
            result={temp.data ?? []}
            windowHours={range.hours}
            format={(v) => `${v.toFixed(1)}°C`}
            label={(m) => m.sensor ?? "sensor"}
          />
        </Card>
        <Card title={`Load average (${range.short})`}>
          <TimeSeries
            result={load.data ?? []}
            windowHours={range.hours}
            format={(v) => v.toFixed(2)}
            label={(m) => m.period ?? "load"}
          />
        </Card>
      </div>
    </div>
  );
}

function AvailabilityTab({ deviceID }: { deviceID: string }) {
  const range = useTimeRange();
  const rtt = useQueryRange(
    `netinv_icmp_rtt_seconds{device_id="${deviceID}"}`,
    range.hours,
    range.stepS,
  );
  const loss = useQueryRange(
    `netinv_icmp_loss_ratio{device_id="${deviceID}"} * 100`,
    range.hours,
    range.stepS,
  );
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card title={`RTT min/avg/max (${range.short})`}>
        <TimeSeries
          result={rtt.data ?? []}
          windowHours={range.hours}
          format={formatMs}
          label={(m) => m.stat ?? "rtt"}
        />
      </Card>
      <Card title={`Packet loss % (${range.short})`}>
        <TimeSeries
          result={loss.data ?? []}
          windowHours={range.hours}
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
    <Card className="overflow-x-auto p-0">
      <table className="w-full min-w-[36rem] text-sm">
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
            <tr
              key={i}
              className="border-b border-slate-100 dark:border-slate-800/60"
            >
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
    <Card className="overflow-x-auto p-0">
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
