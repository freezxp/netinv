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
  useCustomers,
  useImportInterfaceTags,
  useInterfaceSearch,
  useInstantQuery,
  useMetricsLimits,
  utilizationExpr,
  type InterfaceSearchRow,
} from "../../api/hooks";
import {
  Button,
  Card,
  EmptyState,
  Input,
  Select,
  cx,
} from "../../components/ui";
import { formatBps } from "../../lib/format";
import { rateWindow } from "../../api/timerange";

const PAGE = 100;

type SortKey =
  | "device"
  | "name"
  | "customer"
  | "alias"
  | "descr"
  | "speed"
  | "state"
  | "util";

// Which columns the database can order by. The rest are derived from metrics
// and can only be ranked over the rows already fetched.
const SERVER_SORTS = new Set<SortKey>([
  "device",
  "name",
  "customer",
  "alias",
  "descr",
  "speed",
  "state",
]);
const METRIC_SORTS = new Set<SortKey>(["util"]);

const COLUMNS: Array<{ key: SortKey; label: string }> = [
  { key: "device", label: "Device" },
  { key: "name", label: "Interface" },
  { key: "customer", label: "Customer" },
  { key: "alias", label: "Alias" },
  { key: "descr", label: "Description" },
  { key: "speed", label: "Speed" },
  { key: "state", label: "State" },
  { key: "util", label: "Utilization" },
];

export function InterfacesPage() {
  const [typed, setTyped] = useState("");
  const [customer, setCustomer] = useState("");
  const [page, setPage] = useState(0);
  const [importing, setImporting] = useState(false);
  const [upOnly, setUpOnly] = useState(false);
  const [busyOnly, setBusyOnly] = useState(false);
  const q = useDebounced(typed, 300);
  const customers = useCustomers();
  // One sort control for the whole table, even though the columns resolve in
  // two different places: inventory columns sort in the database (whole result
  // set), traffic and utilisation over the rows already fetched, because they
  // come from the metrics store per page. The header says which is which.
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({
    key: "device",
    desc: false,
  });
  const serverSort = SERVER_SORTS.has(sort.key) ? sort.key : "";
  const search = useInterfaceSearch(
    q,
    customer,
    upOnly,
    serverSort,
    sort.desc,
    PAGE,
    page * PAGE,
  );
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

  // Idle is filtered client-side because traffic lives in the metrics store,
  // not the database — there is no column to filter on. It therefore applies
  // to the rows already fetched, which the footer says out loud rather than
  // letting "12 of 4000" look like a broken page.
  const idleHidden = useMemo(
    () =>
      busyOnly
        ? rows.filter(
            (r) => (bpsByIf.get(`${r.device_id}|${r.if_index}`) ?? 0) <= 0,
          ).length
        : 0,
    [busyOnly, rows, bpsByIf],
  );
  const shown = useMemo(() => {
    const base = busyOnly
      ? rows.filter(
          (r) => (bpsByIf.get(`${r.device_id}|${r.if_index}`) ?? 0) > 0,
        )
      : rows;
    if (!METRIC_SORTS.has(sort.key)) return base;
    // Metric columns are sorted here and nowhere else: the database has no
    // traffic to order by. It therefore ranks this page, which the footer
    // states rather than letting "busiest" quietly mean "busiest of the
    // hundred that happened to load".
    const val = (r: InterfaceSearchRow) =>
      sort.key === "util" ? pct(r, bpsByIf) : bps(r, bpsByIf);
    return [...base].sort((a, b) =>
      sort.desc ? val(b) - val(a) : val(a) - val(b),
    );
  }, [rows, busyOnly, sort, bpsByIf]);

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">Interfaces</h1>
        <span className="text-sm text-slate-500">
          {search.isLoading ? "…" : `${total} matching`}
        </span>
        <div className="flex-1" />
        <Button
          variant={upOnly ? "primary" : "ghost"}
          onClick={() => {
            setUpOnly((v) => !v);
            setPage(0);
          }}
          title="Exclude interfaces whose last synced oper status is down"
        >
          {upOnly ? "Hiding down" : "Hide down"}
        </Button>
        <Button
          variant={busyOnly ? "primary" : "ghost"}
          onClick={() => setBusyOnly((v) => !v)}
          title="Exclude interfaces carrying no measurable traffic in the current window"
        >
          {busyOnly ? "Hiding idle" : "Hide idle"}
        </Button>
        <Button variant="ghost" onClick={() => setImporting(true)}>
          Import customers
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
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <span className="text-xs text-slate-500">Customer:</span>
          <Select
            className="w-56"
            value={customer}
            onChange={(e) => {
              setCustomer(e.target.value);
              setPage(0);
            }}
          >
            <option value="">Any customer</option>
            {customers.data?.data.map((c) => (
              <option key={c.customer} value={c.customer}>
                {c.customer} ({c.interfaces})
              </option>
            ))}
          </Select>
          {customers.data?.data.length === 0 && (
            <span className="text-xs text-slate-500">
              nothing tagged yet — use Import customers
            </span>
          )}
        </div>
        <p className="mt-2 text-xs text-slate-500">
          <strong>Hide down</strong> filters in the database, so the total and
          the paging follow it — it uses the oper status recorded by the last
          sync, which is the same value the State column shows.{" "}
          <strong>Hide idle</strong> can only filter the rows already fetched,
          because traffic lives in the metrics store and there is no column to
          filter on; the footer says how many it removed.
        </p>
        <p className="mt-2 text-xs text-slate-500">
          Case-insensitive substring across alias, description, name and
          customer. The customer picker is an exact match instead: a billing run
          must not pick up a different customer whose name merely contains this
          one. Removed interfaces and retired devices are excluded.
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
                {COLUMNS.map((c) => (
                  <th key={c.key} className="px-3 py-2 font-medium">
                    <button
                      className="flex items-center gap-1 uppercase hover:text-slate-700 dark:hover:text-slate-300"
                      title={
                        METRIC_SORTS.has(c.key)
                          ? "Sorts the interfaces on this page — traffic is not a database column"
                          : "Sorts every matching interface, not just this page"
                      }
                      onClick={() => {
                        setPage(0);
                        setSort((s) =>
                          s.key === c.key
                            ? { key: c.key, desc: !s.desc }
                            : // Busiest-first is what anyone opening a traffic
                              // column wants; names read better ascending.
                              { key: c.key, desc: METRIC_SORTS.has(c.key) },
                        );
                      }}
                    >
                      {c.label}
                      {sort.key === c.key && (
                        <span aria-hidden>{sort.desc ? "▼" : "▲"}</span>
                      )}
                    </button>
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

      {importing && <ImportCustomers onClose={() => setImporting(false)} />}

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
            {upOnly && " · down excluded"}
            {busyOnly && ` · ${idleHidden} idle hidden on this page`}
            {METRIC_SORTS.has(sort.key) && " · sorted within this page"}
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
      <td className="px-3 py-1.5">
        {row.customer ? (
          <span className="font-medium">{row.customer}</span>
        ) : (
          <span className="text-slate-400">—</span>
        )}
        {row.tags?.length > 0 && (
          <span className="ml-1 text-xs text-slate-500">
            {row.tags.join(", ")}
          </span>
        )}
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

/**
 * Bulk import of customer and tag assignments.
 *
 * CSV because the list already exists somewhere else — a billing system, a
 * spreadsheet, an old NMS — and retyping it one port at a time is the work
 * worth removing. The result names every row that did not match: an import
 * that silently applies 40 of 50 rows is worse than one that fails.
 */
function ImportCustomers({ onClose }: { onClose: () => void }) {
  const [csv, setCsv] = useState("");
  const imp = useImportInterfaceTags();
  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50 p-4">
      <Card
        title="Import customer assignments"
        className="w-full max-w-[42rem]"
      >
        <p className="text-sm text-slate-500">
          A header row plus one row per interface.{" "}
          <span className="mono">device</span> accepts a name or management
          address, <span className="mono">interface</span> a name or ifIndex,
          and <span className="mono">tags</span> is pipe-separated. A blank cell
          leaves a value unchanged; <span className="mono">-</span> clears it,
          because a blank cell in a spreadsheet almost always means “I did not
          fill this in” rather than “remove this customer”.
        </p>
        <textarea
          className="mono mt-3 h-56 w-full rounded border border-slate-300 bg-transparent p-2 text-xs dark:border-slate-700"
          spellCheck={false}
          value={csv}
          onChange={(e) => setCsv(e.target.value)}
          placeholder={`device,interface,customer,tags
core-sw-1,ge-0/0/1,Acme Ltd,gold|circuit
10.0.0.5,42,Globex,
core-sw-1,ge-0/0/9,-,`}
        />
        <div className="mt-2 flex items-center gap-2">
          <input
            type="file"
            accept=".csv,text/csv"
            className="text-xs"
            onChange={async (e) => {
              const file = e.target.files?.[0];
              if (file) setCsv(await file.text());
            }}
          />
          <div className="flex-1" />
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button
            disabled={!csv.trim() || imp.isPending}
            onClick={() => imp.mutate(csv)}
          >
            {imp.isPending ? "Importing…" : "Import"}
          </Button>
        </div>
        {imp.isError && (
          <div className="mt-2 text-sm text-red-500">
            {(imp.error as Error).message}
          </div>
        )}
        {imp.isSuccess && (
          <div className="mt-2 text-sm">
            <div className="text-green-500">
              Updated {imp.data.updated} of {imp.data.matched} matched
              interfaces.
            </div>
            {imp.data.unmatched.length > 0 && (
              <div className="mt-1 text-amber-500">
                No match ({imp.data.unmatched.length}):{" "}
                <span className="mono text-xs">
                  {imp.data.unmatched.slice(0, 8).join(", ")}
                  {imp.data.unmatched.length > 8 && " …"}
                </span>
              </div>
            )}
            {imp.data.ambiguous.length > 0 && (
              <div className="mt-1 text-amber-500">
                Ambiguous, left alone ({imp.data.ambiguous.length}):{" "}
                <span className="mono text-xs">
                  {imp.data.ambiguous.slice(0, 8).join(", ")}
                </span>
              </div>
            )}
          </div>
        )}
      </Card>
    </div>
  );
}
