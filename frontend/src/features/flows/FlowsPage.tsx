// Flows explorer (doc 30 §5.9, ADR-026): every stored flow, grouped by any
// field, filtered by any field, over any window the store still holds.
//
// The whole query lives in the URL, so a view can be bookmarked or pasted to
// a colleague, and the device page's Flow tab links here with its device
// already filtered. Clicking any value in a table or the Sankey adds it as a
// filter — the way an investigation actually proceeds, one narrowing at a
// time.
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { useDevices } from "../../api/hooks";
import { TIME_RANGES, type RangeKey } from "../../api/timerange";
import { TimeSeries, type PromMatrix } from "../../components/TimeSeries";
import { Button, Card, cx, EmptyState, Input, Select } from "../../components/ui";
import { formatBps, formatBytes } from "../../lib/format";
import {
  useFlowExplore,
  useFlowRecords,
  useFlowStatus,
  type ExploreBody,
  type FlowField,
  type FlowFilter,
  type FlowGroup,
  type FlowLabels,
  type FlowRecord,
} from "./api";
import { Flag } from "./Flag";
import { filterValue, formatCount, fwEvent, renderValue, tcpFlags } from "./format";
import { Sankey } from "./Sankey";

const RANGES: RangeKey[] = ["30m", "1h", "2h", "6h", "12h", "1d", "2d", "1w", "2w", "1mo", "3mo", "6mo", "1y"];
const METRICS = [
  { key: "bytes", label: "Bits/s" },
  { key: "packets", label: "Packets/s" },
  { key: "flows", label: "Flows/s" },
] as const;
type Metric = (typeof METRICS)[number]["key"];

// Filters travel in the URL as f=field~v1|v2, with a leading ! for "not".
function parseFilters(raw: string[]): FlowFilter[] {
  return raw.flatMap((s) => {
    const negate = s.startsWith("!");
    const body = negate ? s.slice(1) : s;
    const i = body.indexOf("~");
    if (i < 1) return [];
    const values = body.slice(i + 1).split("|").filter(Boolean);
    return values.length ? [{ field: body.slice(0, i), negate, values }] : [];
  });
}
function encodeFilter(f: FlowFilter): string {
  return `${f.negate ? "!" : ""}${f.field}~${f.values.join("|")}`;
}

export function FlowsPage() {
  const status = useFlowStatus();
  const [params, setParams] = useSearchParams();
  const [nonce, setNonce] = useState(0);

  const range = (RANGES.includes(params.get("r") as RangeKey) ? params.get("r") : "1h") as RangeKey;
  const groupBy = (params.get("g") ?? "src_addr,dst_addr").split(",").filter(Boolean);
  const metric = (METRICS.some((m) => m.key === params.get("m")) ? params.get("m") : "bytes") as Metric;
  const limit = Number(params.get("n") ?? 10) || 10;
  const device = params.get("d") ?? "";
  const filters = parseFilters(params.getAll("f"));
  const order = params.get("o") === "bytes" ? "bytes" : "time";

  const update = (fn: (p: URLSearchParams) => void) => {
    const next = new URLSearchParams(params);
    fn(next);
    setParams(next, { replace: true });
  };
  const setFilters = (fs: FlowFilter[]) =>
    update((p) => {
      p.delete("f");
      fs.forEach((f) => p.append("f", encodeFilter(f)));
    });
  const addFilter = (field: string, value: string, negate = false) => {
    const existing = filters.find((f) => f.field === field && !!f.negate === negate);
    if (existing) {
      if (existing.values.includes(value)) return;
      setFilters(filters.map((f) => (f === existing ? { ...f, values: [...f.values, value] } : f)));
    } else {
      setFilters([...filters, { field, negate, values: [value] }]);
    }
  };

  const fields = status.data?.fields ?? [];
  const fieldBy = useMemo(() => new Map(fields.map((f) => [f.name, f])), [fields]);
  const rollupDays = status.data?.rollup_days ?? 365;
  const hours = TIME_RANGES.find((r) => r.key === range)?.hours ?? 1;

  const body: ExploreBody | null = useMemo(() => {
    if (!status.data?.enabled) return null;
    const to = new Date(Math.ceil(Date.now() / 60_000) * 60_000);
    const from = new Date(to.getTime() - hours * 3600_000);
    return {
      from: from.toISOString().replace(/\.\d+Z$/, "Z"),
      to: to.toISOString().replace(/\.\d+Z$/, "Z"),
      filters,
      group_by: groupBy,
      metric,
      limit,
      devices: device ? [device] : undefined,
      order_by: order,
    };
    // nonce forces a fresh window on Refresh; the rest is the URL.
  }, [status.data?.enabled, params.toString(), nonce, hours]);

  const explore = useFlowExplore(body);
  // The flow list has its own size: the top-N count is about groups.
  const records = useFlowRecords(body && { ...body, limit: 200 });

  if (status.isLoading) return <div className="text-sm text-slate-500">Loading…</div>;
  if (!status.data?.enabled) return <NotConfigured />;

  const res = explore.data?.result;
  const labels = explore.data?.labels;
  const exporterKeyAt = groupBy.indexOf("exporter");
  const seconds = hours * 3600;

  const keyCtx = (g: FlowGroup) => ({
    labels,
    exporter: exporterKeyAt >= 0 ? g.keys[exporterKeyAt] : singleExporter(labels),
  });
  const groupName = (g: FlowGroup) =>
    g.keys.map((k, i) => renderValue(fieldBy.get(groupBy[i])?.kind ?? "", k, keyCtx(g)).text).join(" · ");
  const metricOf = (g: FlowGroup) => (metric === "bytes" ? g.bytes : metric === "packets" ? g.packets : g.flows);
  const rateFmt = (v: number) =>
    metric === "bytes" ? formatBps(v) : `${formatCount(v)}${metric === "packets" ? " pps" : " fps"}`;
  const totalFmt = (v: number) =>
    metric === "bytes" ? formatBytes(v) : formatCount(v);

  const chart: PromMatrix[] = res
    ? [
        ...(res.series.lines ?? []).map((line, i) => ({
          metric: { name: groupName(res.groups[i]) },
          values: line.map((v, j) => [res.series.times[j], String(v)] as [number, string]),
        })),
        {
          metric: { name: res.groups.length ? "Total (all flows)" : "Total" },
          values: res.series.total.map((v, j) => [res.series.times[j], String(v)] as [number, string]),
        },
      ]
    : [];

  const geo = (status.data.store?.geo_country_ranges ?? 0) > 0;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Flows</h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            Every flow exported to NetInv, kept {status.data.raw_days} days; hourly rollups for{" "}
            {status.data.rollup_days} days. <StoreLine status={status.data} />
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select aria-label="Time range" value={range} onChange={(e) => update((p) => p.set("r", e.target.value))}>
            {RANGES.map((k) => {
              const r = TIME_RANGES.find((x) => x.key === k)!;
              return (
                <option key={k} value={k} disabled={r.hours > rollupDays * 24}>
                  {r.label}
                </option>
              );
            })}
          </Select>
          <Button variant="ghost" onClick={() => setNonce((n) => n + 1)}>
            Refresh
          </Button>
        </div>
      </div>

      <Card>
        <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="eyebrow">Group by</span>
            {groupBy.map((g, i) => (
              <Chip key={g} onRemove={() => update((p) => p.set("g", groupBy.filter((_, j) => j !== i).join(",")))}>
                {fieldBy.get(g)?.label ?? g}
              </Chip>
            ))}
            {groupBy.length < 4 && (
              <Select
                aria-label="Add a group-by field"
                value=""
                onChange={(e) => e.target.value && update((p) => p.set("g", [...groupBy, e.target.value].join(",")))}
              >
                <option value="">+ field</option>
                {fields
                  .filter((f) => f.group && !groupBy.includes(f.name))
                  .map((f) => (
                    <option key={f.name} value={f.name}>
                      {f.label}
                      {f.rollup ? "" : " (raw only)"}
                    </option>
                  ))}
              </Select>
            )}
          </div>
          <div className="flex items-center gap-2">
            <span className="eyebrow">Show</span>
            <Select aria-label="Metric" value={metric} onChange={(e) => update((p) => p.set("m", e.target.value))}>
              {METRICS.map((m) => (
                <option key={m.key} value={m.key}>
                  {m.label}
                </option>
              ))}
            </Select>
            <Select aria-label="How many groups" value={limit} onChange={(e) => update((p) => p.set("n", e.target.value))}>
              {[5, 10, 20, 50, 100].map((n) => (
                <option key={n} value={n}>
                  Top {n}
                </option>
              ))}
            </Select>
          </div>
          <DevicePicker value={device} onChange={(d) => update((p) => (d ? p.set("d", d) : p.delete("d")))} />
        </div>
        <FilterBar fields={fields} filters={filters} onChange={setFilters} />
      </Card>

      {explore.error && (
        <Card>
          <div className="text-sm text-red-600 dark:text-red-400">{(explore.error as Error).message}</div>
        </Card>
      )}

      {res && (
        <>
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <Kpi label="Traffic" value={formatBytes(res.total.bytes)} sub={`${formatBps((res.total.bytes * 8) / seconds)} average`} />
            <Kpi label="Packets" value={formatCount(res.total.packets)} sub={`${formatCount(res.total.packets / seconds)} pps average`} />
            <Kpi label="Flows" value={formatCount(res.total.flows)} />
            <Kpi
              label="Source"
              value={res.table === "flows" ? "Raw flows" : "Hourly rollup"}
              sub={res.table === "flows" ? "every flow, as exported" : "past the raw retention, or a long window"}
            />
          </div>

          <Card title={`${METRICS.find((m) => m.key === metric)!.label} — top ${limit} ${groupBy.length ? "by " + groupBy.map((g) => fieldBy.get(g)?.label ?? g).join(" · ") : ""}`}>
            {res.series.times.length === 0 ? (
              <EmptyState>No flows in this window match.</EmptyState>
            ) : (
              <TimeSeries result={chart} height={260} format={rateFmt} label={(m) => m.name} windowHours={hours} />
            )}
          </Card>

          {groupBy.length === 2 && res.groups.length > 0 && (
            <Card title={`${fieldBy.get(groupBy[0])?.label} → ${fieldBy.get(groupBy[1])?.label}`}>
              <Sankey
                links={res.groups.map((g) => ({ left: g.keys[0], right: g.keys[1], value: metricOf(g) }))}
                leftLabel={(k) => renderValue(fieldBy.get(groupBy[0])?.kind ?? "", k, { labels, exporter: singleExporter(labels) }).text}
                rightLabel={(k) => renderValue(fieldBy.get(groupBy[1])?.kind ?? "", k, { labels, exporter: singleExporter(labels) }).text}
                format={totalFmt}
                onPick={(side, k) => {
                  const f = fieldBy.get(groupBy[side === "left" ? 0 : 1]);
                  if (f) addFilter(f.name, filterValue(f.kind, k));
                }}
              />
            </Card>
          )}

          {groupBy.length > 0 && (
            <Card title="Top groups">
              <GroupTable
                groups={res.groups}
                total={res.total}
                groupBy={groupBy}
                fieldBy={fieldBy}
                labels={labels}
                metric={metric}
                seconds={seconds}
                onFilter={addFilter}
              />
            </Card>
          )}
        </>
      )}

      <Card
        title={
          <div className="flex items-center justify-between">
            <span>Individual flows</span>
            <span className="flex items-center gap-2 normal-case tracking-normal">
              <Select aria-label="Order flows by" value={order} onChange={(e) => update((p) => p.set("o", e.target.value))}>
                <option value="time">Newest first</option>
                <option value="bytes">Largest first</option>
              </Select>
            </span>
          </div>
        }
      >
        {records.error ? (
          <div className="text-sm text-slate-500">{(records.error as Error).message}</div>
        ) : (
          <RecordsTable rows={records.data?.records ?? []} labels={records.data?.labels} fieldBy={fieldBy} onFilter={addFilter} />
        )}
      </Card>

      {geo && (
        <p className="text-xs text-slate-500 dark:text-slate-400">
          Country data:{" "}
          <a href="https://db-ip.com" className="underline" target="_blank" rel="noreferrer">
            IP Geolocation by DB-IP
          </a>{" "}
          (CC BY 4.0). AS names: iptoasn.com (public domain).
        </p>
      )}
    </div>
  );
}

function singleExporter(labels?: FlowLabels): string | undefined {
  const keys = Object.keys(labels?.exporters ?? {});
  return keys.length === 1 ? keys[0] : undefined;
}

function StoreLine({ status }: { status: NonNullable<ReturnType<typeof useFlowStatus>["data"]> }) {
  if (status.error) return <span className="text-amber-600 dark:text-amber-400">Store not reachable: {status.error}</span>;
  const s = status.store;
  if (!s) return null;
  if (s.rows === 0)
    return <span>No flows stored yet — point an exporter at UDP 2055 (NetFlow), 4739 (IPFIX) or 6343 (sFlow).</span>;
  return (
    <span>
      {formatCount(s.rows)} flows on {formatBytes(s.bytes_on_disk)} of disk
      {s.dropped > 0 && <span className="text-amber-600 dark:text-amber-400"> · {formatCount(s.dropped)} dropped by a full queue</span>}.
    </span>
  );
}

function NotConfigured() {
  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-xl font-semibold">Flows</h1>
      <Card>
        <p className="text-sm">
          Raw flow storage is not configured on this deployment, so only the per-interface top-N summaries exist (the
          Flow tab on each device). Set <code>NETINV_FLOW_CH_URL</code> on <code>netinv-flow</code> and the api to a
          ClickHouse server — the Compose deployment includes one — and flows appear here as they arrive. Doc 34 §7
          explains the storage and its sizing.
        </p>
      </Card>
    </div>
  );
}

function Kpi({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <Card>
      <div className="eyebrow">{label}</div>
      <div className="tnum mt-2 text-[24px] font-semibold leading-[1.1] tracking-[-0.02em]">{value}</div>
      {sub && <div className="mt-1 text-xs text-slate-500 dark:text-slate-400">{sub}</div>}
    </Card>
  );
}

function Chip({ children, onRemove, negate }: { children: React.ReactNode; onRemove: () => void; negate?: boolean }) {
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1 rounded-sm border px-2 py-0.5 text-sm",
        negate
          ? "border-red-300 bg-red-50 text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-300"
          : "border-sky-200 bg-sky-50 text-sky-800 dark:border-sky-800 dark:bg-sky-950 dark:text-sky-200",
      )}
    >
      {children}
      <button aria-label="Remove" onClick={onRemove} className="ml-0.5 opacity-60 hover:opacity-100">
        ×
      </button>
    </span>
  );
}

function DevicePicker({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const devices = useDevices({});
  return (
    <div className="flex items-center gap-2">
      <span className="eyebrow">Device</span>
      <Select aria-label="Device" value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">All exporters</option>
        {(devices.data?.data ?? []).map((d) => (
          <option key={d.id} value={d.id}>
            {d.name}
          </option>
        ))}
      </Select>
    </div>
  );
}

function FilterBar({
  fields,
  filters,
  onChange,
}: {
  fields: FlowField[];
  filters: FlowFilter[];
  onChange: (fs: FlowFilter[]) => void;
}) {
  const [field, setField] = useState("addr");
  const [negate, setNegate] = useState(false);
  const [value, setValue] = useState("");
  const label = (n: string) => fields.find((f) => f.name === n)?.label ?? n;
  const add = () => {
    const values = value.split(",").map((v) => v.trim()).filter(Boolean);
    if (!values.length) return;
    onChange([...filters, { field, negate, values }]);
    setValue("");
  };
  return (
    <div className="mt-4 flex flex-col gap-2 border-t border-slate-200 pt-3 dark:border-slate-800">
      <div className="flex flex-wrap items-center gap-2">
        <span className="eyebrow">Filters</span>
        {filters.length === 0 && <span className="text-sm text-slate-500">none — click any value below to add one</span>}
        {filters.map((f, i) => (
          <Chip key={i} negate={f.negate} onRemove={() => onChange(filters.filter((_, j) => j !== i))}>
            {label(f.field)} {f.negate ? "≠" : "="} {f.values.join(", ")}
          </Chip>
        ))}
      </div>
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          add();
        }}
      >
        <Select aria-label="Filter field" value={field} onChange={(e) => setField(e.target.value)}>
          {fields.map((f) => (
            <option key={f.name} value={f.name}>
              {f.label}
            </option>
          ))}
        </Select>
        <Select aria-label="Filter operator" value={negate ? "ne" : "eq"} onChange={(e) => setNegate(e.target.value === "ne")}>
          <option value="eq">is</option>
          <option value="ne">is not</option>
        </Select>
        <Input
          aria-label="Filter value"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder={placeholder(fields.find((f) => f.name === field)?.kind)}
          className="w-64"
        />
        <Button type="submit" variant="ghost" disabled={!value.trim()}>
          Add filter
        </Button>
      </form>
    </div>
  );
}

function placeholder(kind?: string): string {
  switch (kind) {
    case "addr":
    case "exporter":
      return "10.0.0.5 or 10.0.0.0/8, comma for several";
    case "port":
      return "443, 53";
    case "proto":
      return "tcp, udp, icmp or a number";
    case "country":
      return "MY, SG";
    case "as":
      return "13335";
    case "direction":
      return "ingress or egress";
    case "mac":
      return "00:11:22:33:44:55";
  }
  return "value";
}

function Cell({
  kind,
  raw,
  ctx,
  onFilter,
}: {
  kind: string;
  raw: string | number;
  ctx: { labels?: FlowLabels; exporter?: string; bare?: boolean };
  onFilter?: () => void;
}) {
  const r = renderValue(kind, raw, ctx);
  const text = r.text;
  // bare drops the second line where a column is tight and the hint is in
  // the cell's tooltip instead.
  const hint = ctx.bare ? undefined : r.hint;
  const inner = (
    <>
      {kind === "country" && raw !== "" && <Flag code={String(raw)} />}
      <span className={cx(kind === "addr" || kind === "mac" ? "font-mono text-[13px]" : undefined)}>{text}</span>
      {hint && <span className="ml-1.5 text-xs text-slate-500 dark:text-slate-400">{hint}</span>}
    </>
  );
  if (!onFilter || text === "—") return <span className="whitespace-nowrap">{inner}</span>;
  return (
    <button
      onClick={onFilter}
      title="Filter to this value"
      className="whitespace-nowrap text-left hover:text-sky-700 hover:underline dark:hover:text-sky-300"
    >
      {inner}
    </button>
  );
}

function GroupTable({
  groups,
  total,
  groupBy,
  fieldBy,
  labels,
  metric,
  seconds,
  onFilter,
}: {
  groups: FlowGroup[];
  total: FlowGroup;
  groupBy: string[];
  fieldBy: Map<string, FlowField>;
  labels?: FlowLabels;
  metric: Metric;
  seconds: number;
  onFilter: (field: string, value: string) => void;
}) {
  if (groups.length === 0) return <EmptyState>No flows in this window match.</EmptyState>;
  const of = (g: FlowGroup) => (metric === "bytes" ? g.bytes : metric === "packets" ? g.packets : g.flows);
  const whole = of(total) || 1;
  const exporterAt = groupBy.indexOf("exporter");
  const csv = () => {
    const head = [...groupBy.map((g) => fieldBy.get(g)?.label ?? g), "Bytes", "Packets", "Flows", "Average bps"];
    const lines = groups.map((g) =>
      [...g.keys, g.bytes, g.packets, g.flows, Math.round((g.bytes * 8) / seconds)]
        .map((v) => `"${String(v).replace(/"/g, '""')}"`)
        .join(","),
    );
    const blob = new Blob([[head.join(","), ...lines].join("\n")], { type: "text/csv" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "netinv-flows.csv";
    a.click();
    URL.revokeObjectURL(a.href);
  };
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left">
            {groupBy.map((g) => (
              <th key={g} className="py-2 pr-4">
                {fieldBy.get(g)?.label ?? g}
              </th>
            ))}
            <th className="py-2 pr-4 text-right">Traffic</th>
            <th className="py-2 pr-4 text-right">Average</th>
            <th className="py-2 pr-4 text-right">Packets</th>
            <th className="py-2 pr-4 text-right">Flows</th>
            <th className="w-40 py-2">Share</th>
          </tr>
        </thead>
        <tbody>
          {groups.map((g, i) => {
            const ctx = { labels, exporter: exporterAt >= 0 ? g.keys[exporterAt] : singleExporter(labels) };
            const share = of(g) / whole;
            return (
              <tr key={i} className="border-t border-slate-200 dark:border-slate-800">
                {g.keys.map((k, j) => {
                  const f = fieldBy.get(groupBy[j]);
                  return (
                    <td key={j} className="py-1.5 pr-4">
                      <Cell kind={f?.kind ?? ""} raw={k} ctx={ctx} onFilter={f ? () => onFilter(f.name, filterValue(f.kind, k)) : undefined} />
                    </td>
                  );
                })}
                <td className="tnum py-1.5 pr-4 text-right">{formatBytes(g.bytes)}</td>
                <td className="tnum py-1.5 pr-4 text-right">{formatBps((g.bytes * 8) / seconds)}</td>
                <td className="tnum py-1.5 pr-4 text-right">{formatCount(g.packets)}</td>
                <td className="tnum py-1.5 pr-4 text-right">{formatCount(g.flows)}</td>
                <td className="py-1.5">
                  <div className="flex items-center gap-2">
                    <div className="h-1.5 flex-1 rounded-sm bg-slate-100 dark:bg-slate-800">
                      <div className="h-1.5 rounded-sm bg-sky-600 dark:bg-sky-400" style={{ width: `${Math.max(1, share * 100)}%` }} />
                    </div>
                    <span className="tnum w-12 text-right text-xs text-slate-500">{(share * 100).toFixed(1)}%</span>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <div className="mt-3 flex justify-end">
        <Button variant="ghost" onClick={csv}>
          Export CSV
        </Button>
      </div>
    </div>
  );
}

function RecordsTable({
  rows,
  labels,
  fieldBy,
  onFilter,
}: {
  rows: FlowRecord[];
  labels?: FlowLabels;
  fieldBy: Map<string, FlowField>;
  onFilter: (field: string, value: string) => void;
}) {
  if (rows.length === 0) return <EmptyState>No individual flows in this window match.</EmptyState>;
  // Columns that are empty for every row are left out: an exporter that
  // never sends NAT or user would otherwise fill the table with dashes.
  const has = (k: string, empty: unknown[]) => rows.some((r) => !empty.includes(r[k]));
  const optional = [
    { key: "user", label: "User", kind: "text", empty: [""] },
    { key: "app_name", label: "Application", kind: "text", empty: [""] },
    { key: "app_id", label: "App ID", kind: "text", empty: [""] },
    { key: "nat_src_addr", label: "NAT source", kind: "addr", empty: ["::", ""] },
    { key: "src_vlan", label: "VLAN", kind: "number", empty: [0] },
    { key: "dst_country", label: "Dst country", kind: "country", empty: [""] },
    { key: "dst_as_name", label: "Dst AS", kind: "text", empty: [""] },
  ].filter((c) => has(c.key, c.empty));
  const fmtTime = (ms: number) => (ms > 0 ? new Date(ms).toLocaleTimeString() : "—");
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left">
            <th className="py-2 pr-3">Received</th>
            <th className="py-2 pr-3">Exporter</th>
            <th className="py-2 pr-3">In → Out</th>
            <th className="py-2 pr-3">Source</th>
            <th className="py-2 pr-3">Destination</th>
            <th className="py-2 pr-3">Proto</th>
            <th className="py-2 pr-3 text-right">Bytes</th>
            <th className="py-2 pr-3 text-right">Packets</th>
            <th className="py-2 pr-3">Flags</th>
            {optional.map((c) => (
              <th key={c.key} className="py-2 pr-3">
                {c.label}
              </th>
            ))}
            <th className="py-2 pr-3">Format</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => {
            const ctx = { labels, exporter: String(r.exporter) };
            const pick = (field: string, v: string | number) => () => onFilter(field, String(v));
            const dur =
              Number(r.time_end) > 0 && Number(r.time_start) > 0
                ? `${((Number(r.time_end) - Number(r.time_start)) / 1000).toFixed(1)} s`
                : undefined;
            return (
              <tr key={i} className="border-t border-slate-200 dark:border-slate-800">
                <td className="tnum whitespace-nowrap py-1.5 pr-3" title={dur ? `flow lasted ${dur}` : undefined}>
                  {fmtTime(Number(r.received))}
                </td>
                <td className="py-1.5 pr-3" title={String(r.exporter)}>
                  <Cell kind="exporter" raw={r.exporter} ctx={{ ...ctx, bare: true }} onFilter={pick("exporter", r.exporter)} />
                </td>
                <td className="whitespace-nowrap py-1.5 pr-3">
                  <Cell kind="iface" raw={r.in_if} ctx={ctx} onFilter={pick("in_if", r.in_if)} /> →{" "}
                  <Cell kind="iface" raw={r.out_if} ctx={ctx} onFilter={pick("out_if", r.out_if)} />
                </td>
                <td className="whitespace-nowrap py-1.5 pr-3">
                  <Cell kind="addr" raw={r.src_addr} ctx={ctx} onFilter={pick("src_addr", r.src_addr)} />
                  {Number(r.src_port) > 0 && <span className="text-slate-500">:{r.src_port}</span>}
                </td>
                <td className="whitespace-nowrap py-1.5 pr-3">
                  <Cell kind="addr" raw={r.dst_addr} ctx={ctx} onFilter={pick("dst_addr", r.dst_addr)} />
                  {Number(r.dst_port) > 0 && <span className="text-slate-500">:{r.dst_port}</span>}
                </td>
                <td className="py-1.5 pr-3">
                  <Cell kind="proto" raw={r.proto} ctx={ctx} onFilter={pick("proto", r.proto)} />
                </td>
                <td className="tnum whitespace-nowrap py-1.5 pr-3 text-right">{formatBytes(Number(r.bytes))}</td>
                <td className="tnum py-1.5 pr-3 text-right">{formatCount(Number(r.packets))}</td>
                <td className="whitespace-nowrap py-1.5 pr-3 font-mono text-xs">
                  {Number(r.proto) === 6 ? tcpFlags(Number(r.tcp_flags)) : Number(r.fw_event) ? fwEvent(r.fw_event) : "—"}
                </td>
                {optional.map((c) => (
                  <td key={c.key} className="py-1.5 pr-3">
                    <Cell kind={c.kind} raw={r[c.key]} ctx={ctx} onFilter={fieldBy.has(c.key) ? pick(c.key, r[c.key]) : undefined} />
                  </td>
                ))}
                <td className="py-1.5 pr-3 text-xs text-slate-500">
                  {r.type}
                  {Number(r.sampling) > 1 && ` 1:${r.sampling}`}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <p className="mt-2 text-xs text-slate-500">
        Showing {rows.length} flows. Click any value to filter by it. <Link to="/interfaces" className="underline">Interfaces</Link> show
        what SNMP counted; flow shows what it was made of.
      </p>
    </div>
  );
}
