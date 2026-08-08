// Dashboard II panels (doc 30 §2 rows 2–3, 5): Top-N tabs, health heatmap,
// capacity watchlist.
import { useState } from "react";
import { Link } from "react-router-dom";
import { useHeatmap, useTop, useWatchlist } from "../../api/hooks";
import { Card, EmptyState, cx } from "../../components/ui";
import { formatBps } from "../../lib/format";

const topTabs = [
  { key: "if_utilization", label: "Utilization %", fmt: (v: number) => `${v.toFixed(1)}%` },
  { key: "if_traffic", label: "Traffic", fmt: formatBps },
  { key: "if_errors", label: "Errors/s", fmt: (v: number) => v.toFixed(2) },
  { key: "cpu", label: "CPU %", fmt: (v: number) => `${v.toFixed(0)}%` },
];

export function TopNPanel() {
  const [tab, setTab] = useState(topTabs[0]);
  const top = useTop(tab.key);
  return (
    <Card title="Top interfaces / devices">
      <div className="mb-2 flex gap-1">
        {topTabs.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t)}
            className={cx(
              "rounded px-2 py-1 text-xs",
              t.key === tab.key
                ? "bg-sky-600/10 font-medium text-sky-500"
                : "text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-800",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>
      {top.data?.data.length === 0 && (
        <EmptyState>No data for this list yet.</EmptyState>
      )}
      <ol className="flex flex-col gap-1 text-sm">
        {top.data?.data.map((row) => (
          <li key={row.rank} className="flex items-center gap-2">
            <span className="w-5 text-right text-xs text-slate-500">
              {row.rank}.
            </span>
            <Link
              to={`/devices/${row.device_id}${row.if_index ? `?if=${row.if_index}` : ""}`}
              className="flex-1 truncate hover:text-sky-500"
              title={row.device_label ? `${row.device} (${row.device_label})` : row.device}
            >
              {/* An interface row has to say which box it is on: "if 5" alone
                  identifies nothing. Name it, and fall back to the index only
                  when inventory has no name for it. */}
              {row.if_index ? (
                <>
                  <span className="mono">{row.if_name || `if ${row.if_index}`}</span>
                  <span className="text-slate-500"> · {row.device}</span>
                </>
              ) : (
                row.device
              )}
            </Link>
            <span className="mono text-xs">{tab.fmt(row.value)}</span>
          </li>
        ))}
      </ol>
    </Card>
  );
}

const cellColor: Record<string, string> = {
  ok: "var(--status-ok)",
  warning: "var(--status-warning)",
  critical: "var(--status-critical)",
  unreachable: "var(--status-unreachable)",
  muted: "var(--status-muted)",
};

export function HeatmapPanel() {
  const heat = useHeatmap();
  return (
    <Card title="Device health">
      {heat.data?.data.length === 0 && <EmptyState>No devices.</EmptyState>}
      <div className="flex flex-wrap gap-1.5">
        {heat.data?.data.map((c) => (
          <Link
            key={c.device_id}
            to={`/devices/${c.device_id}`}
            title={`${c.device} (${c.site}) — ${c.class}`}
            className="h-6 w-6 rounded-sm transition-transform hover:scale-125"
            style={{ background: cellColor[c.class] }}
          />
        ))}
      </div>
      <div className="mt-3 flex gap-3 text-xs text-slate-500">
        {Object.entries(cellColor).map(([k, v]) => (
          <span key={k} className="inline-flex items-center gap-1">
            <span className="h-2 w-2 rounded-sm" style={{ background: v }} />
            {k}
          </span>
        ))}
      </div>
    </Card>
  );
}

export function WatchlistPanel() {
  const wl = useWatchlist();
  return (
    <Card title="Capacity watchlist (>70% sustained, 24h)">
      {wl.data?.data.length === 0 && (
        <EmptyState>No links above threshold — capacity is healthy.</EmptyState>
      )}
      <div className="flex flex-col gap-2 text-sm">
        {wl.data?.data.map((row) => (
          <div key={row.device_id + row.if_index} className="flex items-center gap-2">
            <Link
              to={`/devices/${row.device_id}?if=${row.if_index}`}
              className="flex-1 truncate hover:text-sky-500"
            >
              <span className="mono">{row.if_name || `if ${row.if_index}`}</span>
              <span className="text-slate-500"> · {row.device}</span>
              {row.site && <span className="text-slate-500"> ({row.site})</span>}
            </Link>
            <div className="h-2 w-32 overflow-hidden rounded bg-slate-200 dark:bg-slate-800">
              <div
                className="h-full"
                style={{
                  width: `${Math.min(row.avg_util_24h, 100)}%`,
                  background:
                    row.avg_util_24h > 85
                      ? "var(--status-critical)"
                      : "var(--status-warning)",
                }}
              />
            </div>
            <span className="mono w-14 text-right text-xs">
              {row.avg_util_24h.toFixed(1)}%
            </span>
          </div>
        ))}
      </div>
    </Card>
  );
}
