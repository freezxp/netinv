// Storage capacity: how much the metrics store holds, how fast it is filling,
// and how long the disk can actually sustain the configured retention.
//
// The last of those is the point. NetInv keeps two years of raw samples by
// default, and nothing previously told an operator whether their disk could.
// The failure is slow and silent — VictoriaMetrics stops accepting writes when
// the volume fills, and collection stops with it — so it has to be visible
// long before it happens.
import { useQuery } from "@tanstack/react-query";
import { api } from "../../api/client";
import { Card, EmptyState } from "../../components/ui";

interface Capacity {
  retention_s: number;
  disk: { used_bytes: number; free_bytes: number };
  metrics: {
    series: number;
    samples: number;
    bytes_per_sample: number;
    samples_per_day: number;
    effective_interval_s: number;
  };
  growth: {
    bytes_per_day: number;
    bytes_per_device_per_year: number;
    days_until_full: number;
    max_retention_s: number;
  };
  devices: number;
  warnings: string[];
}

function bytes(n: number): string {
  if (!isFinite(n) || n < 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

// Spans here run from hours to decades, so a fixed unit would render either
// "0.003 years" or "5418 days". Both are answers nobody can act on.
function span(seconds: number): string {
  if (!isFinite(seconds) || seconds < 0) return "unknown";
  const days = seconds / 86400;
  if (days >= 365 * 2) return `${(days / 365).toFixed(0)} years`;
  if (days >= 365) return `${(days / 365).toFixed(1)} years`;
  if (days >= 60) return `${(days / 30).toFixed(0)} months`;
  if (days >= 1) return `${days.toFixed(0)} days`;
  return `${(seconds / 3600).toFixed(0)} hours`;
}

function Stat({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div className="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
      <div className="text-xs text-slate-500">{label}</div>
      <div className="mt-0.5 text-lg font-semibold tabular-nums">{value}</div>
      {hint && <div className="mt-0.5 text-xs text-slate-500">{hint}</div>}
    </div>
  );
}

export function CapacityTab() {
  const q = useQuery({
    queryKey: ["platform", "capacity"],
    queryFn: () => api<Capacity>("/platform/capacity"),
    refetchInterval: 60_000,
  });

  if (q.isLoading) {
    return <Card title="Storage capacity">Measuring…</Card>;
  }
  if (q.error || !q.data) {
    return (
      <EmptyState>
        Capacity is unavailable — the metrics store did not answer. Collection
        may be affected too; check that every queue has a consumer.
      </EmptyState>
    );
  }

  const c = q.data;
  const total = c.disk.used_bytes + c.disk.free_bytes;
  const usedPct = total > 0 ? (c.disk.used_bytes / total) * 100 : 0;

  // Retention is what was asked for; sustainable is what the disk affords.
  // Showing both side by side is the whole point of the page.
  const sustainable = c.growth.max_retention_s;
  const shortfall = sustainable > 0 && sustainable < c.retention_s;

  return (
    <div className="flex flex-col gap-4">
      {c.warnings.length > 0 && (
        <div className="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-700/60 dark:bg-amber-950/40 dark:text-amber-200">
          <ul className="list-inside list-disc space-y-1">
            {c.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </div>
      )}

      <Card title="How long data can be kept">
        <div className="grid gap-3 sm:grid-cols-2">
          <Stat
            label="Retention setting"
            value={span(c.retention_s)}
            hint="NETINV_VM_RETENTION"
          />
          <Stat
            label="This volume sustains"
            value={span(sustainable)}
            hint={
              shortfall
                ? "less than configured — oldest data will be dropped by disk pressure"
                : "at the current growth rate, using the whole volume"
            }
          />
        </div>
        <p className="mt-3 text-xs text-slate-500">
          {shortfall
            ? "Lower NETINV_VM_RETENTION, add disk, or reduce how many devices are polled. Data dropped for want of space looks identical to data loss."
            : `Comfortable: the disk affords ${span(sustainable)} against a ${span(c.retention_s)} setting. Raising retention beyond that is free only until the disk fills.`}
        </p>
      </Card>

      <Card title="Disk">
        <div className="mb-2 h-2 w-full overflow-hidden rounded bg-slate-200 dark:bg-slate-800">
          <div
            className={
              usedPct > 85
                ? "h-full bg-red-500"
                : usedPct > 60
                  ? "h-full bg-amber-500"
                  : "h-full bg-sky-500"
            }
            style={{ width: `${Math.max(usedPct, 0.5)}%` }}
          />
        </div>
        <div className="grid gap-3 sm:grid-cols-3">
          <Stat label="Metrics data" value={bytes(c.disk.used_bytes)} />
          <Stat label="Free on volume" value={bytes(c.disk.free_bytes)} />
          <Stat
            label="Fills in"
            value={
              c.growth.days_until_full < 0
                ? "unknown"
                : span(c.growth.days_until_full * 86400)
            }
            hint="at the current rate"
          />
        </div>
      </Card>

      <Card title="What is being stored">
        <div className="grid gap-3 sm:grid-cols-3">
          <Stat
            label="Devices polled"
            value={c.devices.toLocaleString()}
            hint={`${c.metrics.series.toLocaleString()} series`}
          />
          <Stat
            label="Samples stored"
            value={c.metrics.samples.toLocaleString()}
            hint={`${c.metrics.bytes_per_sample.toFixed(2)} bytes each, compressed`}
          />
          <Stat
            label="Sample interval"
            value={
              c.metrics.effective_interval_s > 0
                ? `${c.metrics.effective_interval_s.toFixed(0)}s`
                : "—"
            }
            hint={`${Math.round(c.metrics.samples_per_day).toLocaleString()} samples/day`}
          />
        </div>
      </Card>

      <Card title="Growth">
        <div className="grid gap-3 sm:grid-cols-2">
          <Stat label="Per day" value={bytes(c.growth.bytes_per_day)} />
          <Stat
            label="Per device, per year"
            value={bytes(c.growth.bytes_per_device_per_year)}
            hint="the number to plan a fleet with"
          />
        </div>
        <p className="mt-3 text-xs text-slate-500">
          Storage scales with how many devices are polled, not with how long you
          wait — a fleet twice the size fills the disk twice as fast. At this
          rate, 100 devices would need{" "}
          {bytes(
            c.growth.bytes_per_device_per_year *
              100 *
              (c.retention_s / (365 * 86400)),
          )}{" "}
          for the configured retention.
        </p>
      </Card>
    </div>
  );
}
