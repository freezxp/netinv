// The NOC dashboard (doc 30 §2). Sprint 12 rows: status strip, active alerts
// with inline ack, aggregate bandwidth, latency. Top-N/heatmap/watchlist land
// in Sprint 13.
import { Link } from "react-router-dom";
import {
  useAckAlert,
  useAlerts,
  useDashboardSummary,
  useQueryRange,
} from "../../api/hooks";
import { Button, Card, EmptyState, SeverityPill } from "../../components/ui";
import { RangePicker } from "../../components/RangePicker";
import { rateWindow, useTimeRange } from "../../api/timerange";
import { TimeSeries } from "../../components/TimeSeries";
import { HeatmapPanel, TopNPanel, WatchlistPanel } from "./panels";
import {
  formatBps,
  formatDuration,
  formatMs,
  formatPercent,
} from "../../lib/format";

function Stat({
  label,
  value,
  tone,
}: {
  label: string;
  value: string | number;
  tone?: "ok" | "warning" | "critical";
}) {
  const toneClass =
    tone === "critical"
      ? "text-red-500"
      : tone === "warning"
        ? "text-amber-500"
        : tone === "ok"
          ? "text-green-500"
          : "";
  return (
    <Card className="flex-1 min-w-36">
      <div className="text-xs uppercase text-slate-500">{label}</div>
      <div className={`mt-1 text-2xl font-semibold ${toneClass}`}>{value}</div>
    </Card>
  );
}

export function DashboardPage() {
  const summary = useDashboardSummary();
  const alerts = useAlerts();
  const ack = useAckAlert();
  const s = summary.data?.data;

  // Split by site: one fleet-wide line answers "is it busy" but never "where",
  // which is the question a multi-site operator actually has. `or vector(0)`
  // is gone with it — that fallback existed to keep an empty chart from
  // looking broken, and it cannot carry a site label.
  const range = useTimeRange();
  const bandwidth = useQueryRange(
    `sum by (site) (rate(netinv_if_in_octets_total[${rateWindow(range.stepS)}])) * 8`,
    range.hours,
    range.stepS,
  );
  const latency = useQueryRange(
    `netinv_icmp_rtt_seconds{stat="avg"}`,
    range.hours,
    range.stepS,
  );

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-4">
      <div className="flex flex-wrap gap-3">
        <Stat label="Devices up" value={s?.devices.up ?? "…"} tone="ok" />
        <Stat
          label="Unreachable"
          value={s?.devices.unreachable ?? "…"}
          tone={s?.devices.unreachable ? "critical" : undefined}
        />
        <Stat label="Pending" value={s?.devices.pending ?? "…"} />
        <Stat
          label="Critical alerts"
          value={s?.alerts.critical ?? "…"}
          tone={s?.alerts.critical ? "critical" : undefined}
        />
        <Stat
          label="Warnings"
          value={s?.alerts.warning ?? "…"}
          tone={s?.alerts.warning ? "warning" : undefined}
        />
        <Stat
          label="Availability (24h)"
          value={formatPercent(s?.availability_24h)}
          tone="ok"
        />
        <Stat
          label="Throughput in"
          value={s ? formatBps(s.throughput_bps.in ?? 0) : "…"}
        />
      </div>

      <Card title="Active alerts">
        {alerts.data?.data.length === 0 && (
          <EmptyState>No active alerts — all quiet.</EmptyState>
        )}
        <div className="flex flex-col divide-y divide-slate-100 dark:divide-slate-800">
          {alerts.data?.data.map((a) => (
            <div key={a.id} className="flex items-center gap-3 py-2">
              <SeverityPill severity={a.severity} />
              <div className="flex-1">
                <span className="font-medium">{a.rule.name}</span>
                <span className="ml-2 text-sm text-slate-500">
                  {a.labels.device}
                  {a.labels.if_index ? ` · if ${a.labels.if_index}` : ""}
                  {" · "}
                  {formatDuration(a.duration_s)}
                </span>
              </div>
              {a.state === "acknowledged" ? (
                <span className="text-xs text-slate-500">
                  ack: {a.acked?.comment || "—"}
                </span>
              ) : (
                <Button
                  variant="ghost"
                  disabled={ack.isPending}
                  onClick={() =>
                    ack.mutate({ id: a.id, comment: "ack from dashboard" })
                  }
                >
                  Ack
                </Button>
              )}
              {a.device_id && (
                <Link
                  to={`/devices/${a.device_id}${a.labels.if_index ? `?if=${a.labels.if_index}` : ""}`}
                  className="text-sm text-sky-500 hover:underline"
                >
                  Graph →
                </Link>
              )}
            </div>
          ))}
        </div>
      </Card>

      {/* Sits with the charts rather than at the top of the page: the stat
          strip above is server-computed over fixed windows and this control
          does not change it. */}
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-sm font-medium text-slate-500 dark:text-slate-400">
          Graphs
        </h2>
        <RangePicker ariaLabel="Dashboard graph time range" />
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card title={`Bandwidth in, by site (${range.label})`}>
          <TimeSeries
            result={bandwidth.data ?? []}
            windowHours={range.hours}
            format={formatBps}
            label={(m) => m.site || "unassigned"}
          />
        </Card>
        <Card title={`Latency — ICMP avg RTT (${range.label})`}>
          <TimeSeries
            result={latency.data ?? []}
            windowHours={range.hours}
            format={formatMs}
            label={(m) => m.device ?? "device"}
          />
        </Card>
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <TopNPanel />
        <HeatmapPanel />
        <WatchlistPanel />
      </div>
    </div>
  );
}
