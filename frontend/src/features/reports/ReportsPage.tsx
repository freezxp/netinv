// Reports (doc 30 §5b): what a set of interfaces *did* over a period, as
// opposed to what they are doing now.
//
// Selection is the alias/description search rather than a device tree, because
// the question is asked about a set — "every customer circuit", "all the
// uplinks" — and that set is defined by what an operator wrote on the ports,
// not by which chassis they happen to sit in.
import { useState } from "react";
import { api } from "../../api/client";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "../auth/store";
import { useCustomers } from "../../api/hooks";
import { Button, Card, EmptyState, Input, Select } from "../../components/ui";
import { formatBps, formatBytes } from "../../lib/format";

interface ReportRow {
  device_id: string;
  device_name: string;
  customer?: string;
  if_index: number;
  name: string;
  alias: string;
  descr: string;
  speed_bps: number;
  avg_in_bps: number;
  avg_out_bps: number;
  p95_in_bps: number;
  p95_out_bps: number;
  max_in_bps: number;
  max_out_bps: number;
  total_in_bytes: number;
  total_out_bytes: number;
  avg_util_pct: number;
  p95_util_pct: number;
  max_util_pct: number;
}

interface BandwidthReport {
  query: string;
  customer?: string;
  from: string;
  to: string;
  truncated: boolean;
  rows: ReportRow[];
}

// Periods an operator actually reports on. Billing runs to a month, capacity
// reviews to a week; "last 6 hours" is a graph question, not a report one.
const PERIODS = [
  { key: "24h", label: "Last 24 hours", hours: 24 },
  { key: "7d", label: "Last 7 days", hours: 24 * 7 },
  { key: "30d", label: "Last 30 days", hours: 24 * 30 },
  { key: "90d", label: "Last 90 days", hours: 24 * 90 },
] as const;

// The CSV goes through a token-authorized fetch rather than a plain link: the
// session is a bearer token, not a cookie the browser would attach on its own,
// so a link would download a 401 page named like a report.
async function downloadCSV(params: URLSearchParams) {
  params.set("format", "csv");
  const token = useAuthStore.getState().accessToken;
  const res = await fetch(`/api/v1/reports/bandwidth?${params}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) return;
  const blob = await res.blob();
  const name =
    res.headers.get("Content-Disposition")?.match(/filename="([^"]+)"/)?.[1] ??
    "netinv-bandwidth.csv";
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = name;
  a.click();
  URL.revokeObjectURL(a.href);
}

export function ReportsPage() {
  const [typed, setTyped] = useState("");
  const [q, setQ] = useState("");
  const [customer, setCustomer] = useState("");
  const [ranCustomer, setRanCustomer] = useState("");
  const customers = useCustomers();
  const [period, setPeriod] = useState<(typeof PERIODS)[number]["key"]>("7d");
  const hours = PERIODS.find((p) => p.key === period)!.hours;
  const [ran, setRan] = useState(false);

  const params = () => {
    const to = new Date();
    const from = new Date(to.getTime() - hours * 3600_000);
    return new URLSearchParams({
      q,
      customer: ranCustomer,
      from: from.toISOString(),
      to: to.toISOString(),
    });
  };

  // Reports are not run on every keystroke: each one rolls up every matching
  // series over the whole period, which is expensive enough that it should be
  // something an operator asks for on purpose.
  const report = useQuery({
    queryKey: ["report", "bandwidth", q, ranCustomer, period],
    queryFn: () => api<BandwidthReport>(`/reports/bandwidth?${params()}`),
    enabled: ran,
    staleTime: 60_000,
  });

  const run = () => {
    setQ(typed);
    setRanCustomer(customer);
    setRan(true);
  };

  const rows = report.data?.rows ?? [];

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">Reports</h1>
        <span className="text-sm text-slate-500">Interface bandwidth</span>
      </div>

      <Card>
        <div className="flex flex-wrap items-end gap-2">
          <label className="flex-1 text-xs text-slate-500">
            Interfaces matching alias, description or name
            <Input
              className="mt-1 w-full"
              value={typed}
              autoFocus
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && run()}
              placeholder="e.g. CUSTOMER, uplink, WAN — blank reports on everything"
            />
          </label>
          <label className="text-xs text-slate-500">
            Customer
            <Select
              className="mt-1 w-52"
              value={customer}
              onChange={(e) => setCustomer(e.target.value)}
            >
              <option value="">Any customer</option>
              {customers.data?.data.map((c) => (
                <option key={c.customer} value={c.customer}>
                  {c.customer} ({c.interfaces})
                </option>
              ))}
            </Select>
          </label>
          <label className="text-xs text-slate-500">
            Period
            <Select
              className="mt-1 w-40"
              value={period}
              onChange={(e) =>
                setPeriod(e.target.value as (typeof PERIODS)[number]["key"])
              }
            >
              {PERIODS.map((p) => (
                <option key={p.key} value={p.key}>
                  {p.label}
                </option>
              ))}
            </Select>
          </label>
          <Button onClick={run} disabled={report.isFetching}>
            {report.isFetching ? "Running…" : "Run report"}
          </Button>
          <Button
            variant="ghost"
            disabled={!report.data || rows.length === 0}
            onClick={() => downloadCSV(params())}
          >
            Download CSV
          </Button>
        </div>
        <p className="mt-2 text-xs text-slate-500">
          95th percentile is the figure most transit and transport contracts
          bill on — it discards the top 5% of samples, so a short burst does not
          set the number. Totals are what crossed the interface over the whole
          period; utilization follows the busier direction, because a link is
          congested when either direction is.
        </p>
      </Card>

      {report.isError && (
        <Card>
          <div className="text-sm text-red-500">
            {(report.error as Error).message}
          </div>
        </Card>
      )}

      {report.data && (
        <>
          <div className="text-xs text-slate-500">
            {new Date(report.data.from).toLocaleString()} —{" "}
            {new Date(report.data.to).toLocaleString()}
            {report.data.customer && ` · ${report.data.customer}`} ·{" "}
            {rows.length} interface{rows.length === 1 ? "" : "s"}
            {report.data.truncated && (
              <span className="ml-2 text-amber-500">
                truncated — narrow the search to report on the rest
              </span>
            )}
          </div>
          <Card className="overflow-x-auto p-0">
            {rows.length === 0 ? (
              <EmptyState>
                Nothing matched
                {report.data.query ? ` “${report.data.query}”` : ""}.
              </EmptyState>
            ) : (
              <table className="w-full min-w-[60rem] text-sm">
                <thead>
                  <tr className="border-b border-slate-200 text-left text-xs uppercase text-slate-500 dark:border-slate-800">
                    {[
                      "Customer",
                      "Device",
                      "Interface",
                      "Alias",
                      "Avg in / out",
                      "95th in / out",
                      "Peak in / out",
                      "Total in / out",
                      "Util 95th",
                    ].map((h) => (
                      <th key={h} className="px-3 py-2 font-medium">
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r) => (
                    <tr
                      key={`${r.device_id}-${r.if_index}`}
                      className="border-b border-slate-100 dark:border-slate-800/60"
                    >
                      <td className="px-3 py-1.5">
                        {r.customer || (
                          <span className="text-slate-400">—</span>
                        )}
                      </td>
                      <td className="px-3 py-1.5">{r.device_name}</td>
                      <td className="mono px-3 py-1.5 text-xs">{r.name}</td>
                      <td className="px-3 py-1.5">{r.alias || "—"}</td>
                      <td className="px-3 py-1.5 text-slate-500">
                        {formatBps(r.avg_in_bps)} / {formatBps(r.avg_out_bps)}
                      </td>
                      <td className="px-3 py-1.5 font-medium">
                        {formatBps(r.p95_in_bps)} / {formatBps(r.p95_out_bps)}
                      </td>
                      <td className="px-3 py-1.5 text-slate-500">
                        {formatBps(r.max_in_bps)} / {formatBps(r.max_out_bps)}
                      </td>
                      <td className="px-3 py-1.5">
                        {formatBytes(r.total_in_bytes)} /{" "}
                        {formatBytes(r.total_out_bytes)}
                      </td>
                      <td className="px-3 py-1.5">
                        {r.p95_util_pct < 0 ? (
                          <span
                            className="text-xs text-slate-400"
                            title="ifSpeed is unknown for this interface, so a percentage would be meaningless"
                          >
                            speed unknown
                          </span>
                        ) : (
                          <span
                            className={
                              r.p95_util_pct >= 90
                                ? "text-red-500"
                                : r.p95_util_pct >= 80
                                  ? "text-amber-500"
                                  : ""
                            }
                          >
                            {r.p95_util_pct.toFixed(1)}%
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Card>
        </>
      )}

      {!report.data && !report.isFetching && (
        <Card>
          <EmptyState>
            Choose a period and run the report. Leave the search blank to cover
            every interface.
          </EmptyState>
        </Card>
      )}
    </div>
  );
}
