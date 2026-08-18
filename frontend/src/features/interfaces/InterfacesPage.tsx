// Interfaces (doc 30 §9): find a port across the whole fleet by what an
// operator wrote on it, and see what it is carrying.
//
// Alias is the field people actually curate — circuit ids, customer names, the
// far end of a link — and it was only readable one device at a time. "Which
// port is the London circuit on" was a question the product could not answer
// unless you already knew the answer.
import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  useInterfaceSearch,
  useInstantQuery,
  useMetricsLimits,
  utilizationExpr,
  type InterfaceSearchRow,
} from "../../api/hooks";
import { Button, Card, EmptyState, Input, cx } from "../../components/ui";
import { formatBps } from "../../lib/format";
import { rateWindow } from "../../api/timerange";

const PAGE = 100;

export function InterfacesPage() {
  const [typed, setTyped] = useState("");
  const [page, setPage] = useState(0);
  const q = useDebounced(typed, 300);
  const search = useInterfaceSearch(q, PAGE, page * PAGE);
  const rows = search.data?.data ?? [];
  const total = search.data?.total ?? 0;

  // Utilization is asked for only about the rows on screen. A fleet-wide query
  // would return a series per interface in the estate to fill a hundred cells.
  const deviceIDs = useMemo(
    () => [...new Set(rows.map((r) => r.device_id))],
    [rows],
  );
  const pollS = useMetricsLimits().data?.poll_interval_s ?? 0;
  const expr = utilizationExpr(deviceIDs, rateWindow(60, pollS));
  const util = useInstantQuery(expr, expr !== "");
  const bpsByIf = useMemo(() => {
    const m = new Map<string, number>();
    for (const s of util.data ?? []) {
      m.set(`${s.metric.device_id}|${s.metric.if_index}`, Number(s.value[1]));
    }
    return m;
  }, [util.data]);

  const [sortByUtil, setSortByUtil] = useState(false);
  const shown = useMemo(() => {
    if (!sortByUtil) return rows;
    // Sorting is over the current page only, and says so in the UI: the server
    // orders by device and ifIndex, and re-ranking the whole estate by
    // utilization would mean asking the metrics store first and the database
    // second. Claiming "busiest interfaces" while showing the busiest hundred
    // alphabetically would be a lie.
    return [...rows].sort(
      (a, b) =>
        pct(b, bpsByIf) - pct(a, bpsByIf) || bps(b, bpsByIf) - bps(a, bpsByIf),
    );
  }, [rows, sortByUtil, bpsByIf]);

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">Interfaces</h1>
        <span className="text-sm text-slate-500">
          {search.isLoading ? "…" : `${total} matching`}
        </span>
        <div className="flex-1" />
        <Button
          variant="ghost"
          onClick={() => setSortByUtil((v) => !v)}
          title="Sort the interfaces on this page by how busy they are"
        >
          {sortByUtil ? "Sorted by utilization" : "Sort by utilization"}
        </Button>
      </div>

      <Card>
        <Input
          className="w-full"
          autoFocus
          value={typed}
          onChange={(e) => {
            setTyped(e.target.value);
            setPage(0);
          }}
          placeholder="Search alias, description or interface name — e.g. uplink, WAN, ge-0/0/1"
        />
        <p className="mt-2 text-xs text-slate-500">
          Case-insensitive substring across all three fields, because whoever
          labelled the port may have put it in any of them. Removed interfaces
          and retired devices are excluded.
        </p>
      </Card>

      <Card className="overflow-x-auto p-0">
        {rows.length === 0 && !search.isLoading ? (
          <EmptyState>
            {q
              ? `Nothing matches “${q}”.`
              : "No interfaces yet — devices populate this on their first sync."}
          </EmptyState>
        ) : (
          <table className="w-full min-w-[52rem] text-sm">
            <thead>
              <tr className="border-b border-slate-200 text-left text-xs uppercase text-slate-500 dark:border-slate-800">
                {[
                  "Device",
                  "Interface",
                  "Alias",
                  "Description",
                  "Speed",
                  "State",
                  "Utilization",
                ].map((h) => (
                  <th key={h} className="px-3 py-2 font-medium">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => (
                <Row
                  key={r.id}
                  row={r}
                  bpsByIf={bpsByIf}
                  loading={util.isLoading}
                />
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {total > PAGE && (
        <div className="flex items-center justify-between text-sm">
          <Button
            variant="ghost"
            disabled={page === 0}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
          >
            ← Previous
          </Button>
          <span className="text-slate-500">
            {page * PAGE + 1}–{Math.min((page + 1) * PAGE, total)} of {total}
            {sortByUtil && " · sorted within this page"}
          </span>
          <Button
            variant="ghost"
            disabled={(page + 1) * PAGE >= total}
            onClick={() => setPage((p) => p + 1)}
          >
            Next →
          </Button>
        </div>
      )}
    </div>
  );
}

function bps(r: InterfaceSearchRow, m: Map<string, number>) {
  return m.get(`${r.device_id}|${r.if_index}`) ?? 0;
}

// Percent is only meaningful when the speed is known. ifSpeed is absent or
// wrong on plenty of real ports — a PPPoE session has none — so an unknown
// speed must read as unknown rather than as 0% or as a divide by zero.
function pct(r: InterfaceSearchRow, m: Map<string, number>) {
  if (!r.speed_bps) return -1;
  return (bps(r, m) / r.speed_bps) * 100;
}

function Row({
  row,
  bpsByIf,
  loading,
}: {
  row: InterfaceSearchRow;
  bpsByIf: Map<string, number>;
  loading: boolean;
}) {
  const rate = bpsByIf.get(`${row.device_id}|${row.if_index}`);
  const p = pct(row, bpsByIf);
  const up = row.oper_status === 1;
  return (
    <tr className="border-b border-slate-100 dark:border-slate-800/60">
      <td className="px-3 py-1.5">
        <Link
          to={`/devices/${row.device_id}`}
          className="text-sky-500 hover:underline"
        >
          {row.device_name}
        </Link>
      </td>
      <td className="px-3 py-1.5">
        <Link
          to={`/devices/${row.device_id}?if=${row.if_index}`}
          className="mono text-xs text-sky-500 hover:underline"
          title="Open this interface's graph on the device"
        >
          {row.name || row.if_index}
        </Link>
      </td>
      <td className="px-3 py-1.5">{row.alias || "—"}</td>
      <td
        className="max-w-[16rem] truncate px-3 py-1.5 text-slate-500"
        title={row.descr}
      >
        {row.descr || "—"}
      </td>
      <td className="px-3 py-1.5 text-slate-500">
        {row.speed_bps ? formatBps(row.speed_bps) : "—"}
      </td>
      <td className="px-3 py-1.5">
        <span className={up ? "text-green-500" : "text-red-500"}>
          {up ? "up" : row.oper_status === 2 ? "down" : "—"}
        </span>
        {row.admin_status === 2 && (
          <span className="ml-1 text-xs text-slate-500">(admin down)</span>
        )}
      </td>
      <td className="px-3 py-1.5">
        {loading && rate === undefined ? (
          <span className="text-xs text-slate-400">…</span>
        ) : rate === undefined ? (
          <span
            className="text-xs text-slate-400"
            title="No traffic samples in the last window"
          >
            no data
          </span>
        ) : (
          <div className="flex items-center gap-2">
            <div className="h-1.5 w-20 shrink-0 rounded bg-slate-200 dark:bg-slate-800">
              {p >= 0 && (
                <div
                  className={cx(
                    "h-1.5 rounded",
                    p >= 90
                      ? "bg-red-500"
                      : p >= 80
                        ? "bg-amber-500"
                        : "bg-sky-500",
                  )}
                  style={{ width: `${Math.min(100, Math.max(1, p))}%` }}
                />
              )}
            </div>
            <span className="mono text-xs">
              {p >= 0 ? `${p.toFixed(1)}%` : "speed unknown"}
            </span>
            <span className="text-xs text-slate-500">{formatBps(rate)}</span>
          </div>
        )}
      </td>
    </tr>
  );
}

// Typing in a search box should not issue a request per keystroke: each one
// hits Postgres with an ILIKE across every interface in the estate.
function useDebounced(value: string, ms: number) {
  const [held, setHeld] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setHeld(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return held;
}
