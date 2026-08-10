// The NOC dashboard (doc 30 §2), assembled from a per-user layout rather than
// a fixed sequence of panels. What belongs on screen differs between someone
// watching capacity, someone watching one site, and someone who wants the
// weathermap up all day — see ./layout.ts.
import { useState } from "react";
import { Link } from "react-router-dom";
import {
  useAckAlert,
  useAlerts,
  useDashboardSummary,
  useQueryRange,
} from "../../api/hooks";
import { Button, Card, EmptyState, SeverityPill } from "../../components/ui";
import { RangePicker } from "../../components/RangePicker";
import { FlowPanel, MetricPanel, WeathermapPanel } from "./CustomPanels";
import { Customise } from "./Customise";
import {
  DEFAULT_LAYOUT,
  useDashboardLayout,
  type DashboardLayout,
  type Panel,
} from "./layout";
import { rateWindow, useTimeRange } from "../../api/timerange";
import { useMetricsLimits } from "../../api/hooks";
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

// Each panel is a component so the layout can order them freely. They were
// inline in one long return, which is why the dashboard's contents were fixed
// at build time.
function StatusPanel() {
  const summary = useDashboardSummary();
  const s = summary.data?.data;
  return (
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
      <Stat
        label="Throughput out"
        value={s ? formatBps(s.throughput_bps.out ?? 0) : "…"}
      />
    </div>
  );
}

function AlertsPanel() {
  const alerts = useAlerts();
  const ack = useAckAlert();
  return (
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
  );
}

function BandwidthPanel({ dir }: { dir: "in" | "out" }) {
  const range = useTimeRange();
  const pollS = useMetricsLimits().data?.poll_interval_s ?? 0;
  const q = useQueryRange(
    `sum by (site) (rate(netinv_if_${dir}_octets_total[${rateWindow(range.stepS, pollS)}])) * 8`,
    range.hours,
    range.stepS,
  );
  return (
    <Card title={`Bandwidth ${dir}, by site (${range.short})`}>
      <TimeSeries
        result={q.data ?? []}
        windowHours={range.hours}
        format={formatBps}
        label={(m) => m.site || "unassigned"}
      />
    </Card>
  );
}

function LatencyPanel() {
  const range = useTimeRange();
  const q = useQueryRange(
    `netinv_icmp_rtt_seconds{stat="avg"}`,
    range.hours,
    range.stepS,
  );
  return (
    <Card title={`Latency — ICMP avg RTT (${range.short})`}>
      <TimeSeries
        result={q.data ?? []}
        windowHours={range.hours}
        format={formatMs}
        label={(m) => m.device ?? "device"}
      />
    </Card>
  );
}

function renderPanel(p: Panel) {
  switch (p.kind) {
    case "status":
      return <StatusPanel />;
    case "alerts":
      return <AlertsPanel />;
    case "bandwidth_in":
      return <BandwidthPanel dir="in" />;
    case "bandwidth_out":
      return <BandwidthPanel dir="out" />;
    case "latency":
      return <LatencyPanel />;
    case "topn":
      return <TopNPanel />;
    case "heatmap":
      return <HeatmapPanel />;
    case "watchlist":
      return <WatchlistPanel />;
    case "weathermap":
      return <WeathermapPanel panel={p} />;
    case "metric":
      return <MetricPanel panel={p} />;
    case "flow":
      return <FlowPanel panel={p} />;
  }
}

export function DashboardPage() {
  const { layout, save, loading } = useDashboardLayout();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<DashboardLayout | null>(null);
  const shown = draft ?? layout;

  const apply = (l: DashboardLayout) => {
    setDraft(l);
    save.mutate(l);
  };

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <h1 className="text-xl font-semibold">Dashboard</h1>
        <div className="flex items-center gap-2">
          <RangePicker ariaLabel="Dashboard graph time range" />
          <Button variant="ghost" onClick={() => setEditing((v) => !v)}>
            {editing ? "Done" : "Customise"}
          </Button>
        </div>
      </div>

      {editing && (
        <>
          <Customise
            layout={shown}
            onChange={apply}
            onClose={() => setEditing(false)}
            saving={save.isPending}
          />
          <div>
            <Button variant="ghost" onClick={() => apply(DEFAULT_LAYOUT)}>
              Reset to default
            </Button>
          </div>
        </>
      )}

      {loading ? (
        <Card title="Dashboard">Loading your layout…</Card>
      ) : shown.panels.length === 0 ? (
        <EmptyState>
          No panels. Choose <strong>Customise</strong> to add some, or reset to
          the default layout.
        </EmptyState>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {shown.panels.map((p) => (
            <div key={p.id} className={p.wide ? "lg:col-span-2" : undefined}>
              {renderPanel(p)}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
