import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type {
  Alert,
  DashboardSummary,
  Device,
  LoginResponse,
  Paged,
  Site,
} from "./types";
import { useAuthStore } from "../features/auth/store";

export function useLogin() {
  const setSession = useAuthStore((s) => s.setSession);
  return useMutation({
    mutationFn: (creds: { username: string; password: string }) =>
      api<LoginResponse>("/auth/login", {
        method: "POST",
        body: JSON.stringify(creds),
      }),
    onSuccess: (res) => setSession(res.access_token, res.user),
  });
}

export function useLogout() {
  const clear = useAuthStore((s) => s.clear);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api<void>("/auth/logout", { method: "POST" }),
    onSettled: () => {
      clear();
      qc.clear();
    },
  });
}

export interface DeviceFilters {
  q?: string;
  site?: string;
  status?: string;
  cursor?: string;
}

export function useDevices(filters: DeviceFilters) {
  const params = new URLSearchParams();
  if (filters.q) params.set("q", filters.q);
  if (filters.cursor) params.set("cursor", filters.cursor);
  const filterParts = [];
  if (filters.site) filterParts.push(`site:eq:${filters.site}`);
  if (filters.status) filterParts.push(`status:eq:${filters.status}`);
  if (filterParts.length) params.set("filter", filterParts.join(","));
  return useQuery({
    queryKey: ["devices", filters],
    queryFn: () => api<Paged<Device>>(`/devices?${params}`),
    placeholderData: (prev) => prev,
  });
}

export function useSites() {
  return useQuery({
    queryKey: ["sites"],
    queryFn: () => api<Paged<Site>>("/sites"),
    staleTime: 60_000,
  });
}

export function useAlerts() {
  return useQuery({
    queryKey: ["alerts"],
    queryFn: () => api<{ data: Alert[] }>("/alerts"),
    refetchInterval: 15_000, // live panel cadence (doc 14)
  });
}

export function useDashboardSummary() {
  return useQuery({
    queryKey: ["dashboard", "summary"],
    queryFn: () => api<DashboardSummary>("/dashboard/summary"),
    refetchInterval: 30_000,
  });
}

interface Wrapped<T> {
  as_of: string;
  data: T;
}

export interface TopRow {
  rank: number;
  value: number;
  device_id: string;
  /** The device's own sysName where it has one. */
  device: string;
  /** The operator's label, only when it differs from sysName. */
  device_label?: string;
  site: string;
  if_index?: string;
  /** Interface name from inventory; metrics carry only the index. */
  if_name?: string;
}

export function useTop(list: string) {
  return useQuery({
    queryKey: ["dashboard", "top", list],
    queryFn: () => api<Wrapped<TopRow[]>>(`/dashboard/top?list=${list}`),
    refetchInterval: 30_000,
  });
}

export interface HeatCell {
  device_id: string;
  device: string;
  site: string;
  class: "ok" | "warning" | "critical" | "unreachable" | "muted";
}

export type DeviceHealthMap = Record<
  string,
  { cpu?: number; memory?: number; temp?: number; load?: number }
>;

// One payload with the latest CPU/memory/temperature for every device, so the
// inventory list shows live stats without a query per row.
export function useDeviceHealth() {
  return useQuery({
    queryKey: ["dashboard", "device-health"],
    queryFn: () => api<Wrapped<DeviceHealthMap>>("/dashboard/device-health"),
    refetchInterval: 30_000,
  });
}

export function useHeatmap() {
  return useQuery({
    queryKey: ["dashboard", "heatmap"],
    queryFn: () => api<Wrapped<HeatCell[]>>("/dashboard/heatmap"),
    refetchInterval: 30_000,
  });
}

export function useWatchlist() {
  return useQuery({
    queryKey: ["dashboard", "watchlist"],
    queryFn: () =>
      api<
        Wrapped<
          Array<{
            device_id: string;
            device: string;
            device_label?: string;
            if_index: string;
            if_name?: string;
            site: string;
            avg_util_24h: number;
          }>
        >
      >("/dashboard/watchlist"),
    refetchInterval: 60_000,
  });
}

export function useDevice(id: string) {
  return useQuery({
    queryKey: ["device", id],
    queryFn: () => api<Device>(`/devices/${id}`),
  });
}

export interface InterfaceRow {
  id: string;
  if_index: number;
  name: string;
  alias: string;
  speed_bps: number;
  mtu: number;
  admin_status: number;
  oper_status: number;
  state: string;
  monitor: boolean;
  /** False for a port never seen in service; those don't raise down alerts. */
  ever_up: boolean;
}

export function useDeviceInterfaces(id: string) {
  return useQuery({
    queryKey: ["device", id, "interfaces"],
    queryFn: () => api<{ data: InterfaceRow[] }>(`/devices/${id}/interfaces`),
    refetchInterval: 60_000,
  });
}

export interface HistoryRow {
  object_kind: string;
  object_id: string;
  field: string;
  old_value: string;
  new_value: string;
  change_kind: string;
  detected_at: string;
}

export function useDeviceHistory(id: string) {
  return useQuery({
    queryKey: ["device", id, "history"],
    queryFn: () => api<{ data: HistoryRow[] }>(`/devices/${id}/history`),
  });
}

export function useDeviceAlerts(id: string) {
  return useQuery({
    queryKey: ["alerts", "device", id],
    queryFn: () => api<{ data: Alert[] }>(`/alerts?filter=device:eq:${id}`),
    refetchInterval: 30_000,
  });
}

export function useSyncNow() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api<{ job_id: string }>(`/devices/${id}/sync`, { method: "POST" }),
    onSuccess: (_r, id) =>
      setTimeout(
        () => qc.invalidateQueries({ queryKey: ["device", id] }),
        4000,
      ),
  });
}

export function useAckAlert() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, comment }: { id: string; comment: string }) =>
      api<Alert>(`/alerts/${id}/ack`, {
        method: "POST",
        body: JSON.stringify({ comment }),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
  });
}

// useMetricsLimits reports how far back the API will serve a range query, and
// how often devices are polled.
//
// The server rejects anything beyond its ceiling outright — it does not clamp
// — so a range picker that offers more than this produces errors rather than
// short graphs. The ceiling tracks the deployment's retention and is therefore
// not knowable at build time. The poll interval travels with it because rate()
// lookbacks must exceed the cadence, and both are deployment settings.
export function useMetricsLimits() {
  return useQuery({
    queryKey: ["metrics", "limits"],
    queryFn: () =>
      api<{ max_range_s: number; poll_interval_s: number }>("/metrics/limits"),
    staleTime: 60 * 60_000, // a deploy-time setting; no reason to re-ask often
  });
}

interface RangeMatrix {
  data: {
    result: Array<{
      metric: Record<string, string>;
      values: Array<[number, string]>;
    }>;
  };
}

// trafficExpr returns interface throughput as two labelled series.
//
// label_set is load-bearing: multiplying by 8 strips __name__ from both sides,
// leaving them with identical label sets, and `or` then keeps the right-hand
// series only where the left has no match — so the out direction disappeared
// entirely and the chart silently showed in twice. The explicit dir label
// keeps them distinct.
//
// `window` must track the query step (see rateWindow in timerange.ts). A fixed
// [5m] against a 30-minute step samples a sixth of each bucket and reports it
// as the whole thing, which makes a week of traffic look like noise.
export function trafficExpr(
  deviceID: string,
  ifIndex: number | string,
  window = "5m",
) {
  const sel = `{device_id="${deviceID}",if_index="${ifIndex}"}`;
  return (
    `label_set(rate(netinv_if_in_octets_total${sel}[${window}]) * 8, "dir", "in")` +
    ` or label_set(rate(netinv_if_out_octets_total${sel}[${window}]) * 8, "dir", "out")`
  );
}

// seriesExpr combines several metrics into one range query while keeping them
// apart.
//
// `or` is a set operator that matches on labels *excluding* __name__, so two
// metrics sharing only device_id collapse into one series and the second
// silently disappears — a used/total pair comes back as used alone, and the
// total reads as unknown. Each side gets an explicit `series` label to be
// matched on instead.
export function seriesExpr(
  selector: string,
  parts: Array<[name: string, metric: string]>,
) {
  return parts
    .map(
      ([name, metric]) =>
        `label_set(${metric}${selector}, "series", "${name}")`,
    )
    .join(" or ");
}

// --- flow (doc 34) --------------------------------------------------------
//
// Flow series are shaped unlike every other metric here, and getting it wrong
// is silent rather than loud. `netinv_flow_bytes` is **not cumulative**: each
// sample holds the bytes counted during that one aggregation interval, so
// rate() — the reflex everywhere else in this file — would divide a per-minute
// total by the lookback and under-report by that factor without erroring.
//
// sum_over_time over the step is the correct reduction: total bytes in the
// window, divided by the window, is average bits per second however the step
// is sized. It also stays honest when a bucket drops out of the top N for part
// of the window — those minutes contribute nothing rather than being averaged
// away, which is what actually happened.
// Not exported: callers should size windows with flowWindow rather than
// reimplementing the floor it enforces.
const FLOW_INTERVAL_S = 60;

export type FlowDimension = "talker" | "conversation" | "application";

// An empty exporter means every exporter, which is what a fleet-wide dashboard
// panel wants — the busiest hosts on the network, not on one device.
export function flowSelector(
  exporter: string,
  dimension: FlowDimension,
  ifIndex?: string,
) {
  const parts = [`dimension="${dimension}"`];
  if (exporter) parts.unshift(`exporter="${exporter}"`);
  if (ifIndex) parts.push(`if_index="${ifIndex}"`);
  return `{${parts.join(",")}}`;
}

export interface FlowRow {
  value: string;
  bytes: number;
}

// toFlowRows ranks a flow vector by volume over the queried range.
//
// Ranking on total bytes rather than the newest sample is deliberate: top-N
// membership churns between intervals, so ordering by the latest value would
// reshuffle the table every minute on ordinary jitter and make it unreadable
// exactly when someone is trying to read it.
export function toFlowRows(
  result: Array<{ metric: Record<string, string>; value: [number, string] }>,
): FlowRow[] {
  return result
    .map((r) => ({
      value: r.metric.value ?? "—",
      bytes: parseFloat(r.value[1]),
    }))
    .filter((r) => isFinite(r.bytes) && r.bytes > 0)
    .sort((a, b) => b.bytes - a.bytes);
}

// flowWindow never goes below the aggregation interval: a window shorter than
// one interval can fall between samples and return nothing at all.
export function flowWindow(stepS: number) {
  return Math.max(stepS, FLOW_INTERVAL_S);
}

// flowRateExpr gives average bits/sec per bucket, summed across interfaces
// when no single one is selected.
export function flowRateExpr(selector: string, windowS: number) {
  return (
    `sum by (value) (sum_over_time(netinv_flow_bytes${selector}[${windowS}s]))` +
    ` * 8 / ${windowS}`
  );
}

// flowTotalExpr gives total bytes per bucket over the whole visible range,
// which is what ranks a top-N table — ranking on the latest sample alone would
// reorder the table every minute on ordinary jitter.
export function flowTotalExpr(selector: string, rangeS: number) {
  return `sum by (value) (sum_over_time(netinv_flow_bytes${selector}[${rangeS}s]))`;
}

interface Vector {
  data: {
    result: Array<{ metric: Record<string, string>; value: [number, string] }>;
  };
}

// useInstantQuery evaluates an expression at a single point in time.
export function useInstantQuery(expr: string, enabled = true) {
  return useQuery({
    queryKey: ["instant", expr],
    queryFn: async () => {
      const params = new URLSearchParams({ query: expr });
      const res = await api<Vector>(`/metrics/query?${params}`);
      return res.data.result;
    },
    enabled,
    refetchInterval: 30_000,
  });
}

// useQueryRange fetches a MetricsQL range through the scope-guarded proxy.
export function useQueryRange(expr: string, rangeHours: number, stepS = 60) {
  return useQuery({
    queryKey: ["range", expr, rangeHours, stepS],
    queryFn: async () => {
      const end = Math.floor(Date.now() / 1000);
      const start = end - rangeHours * 3600;
      const params = new URLSearchParams({
        query: expr,
        start: String(start),
        end: String(end),
        step: `${stepS}s`,
      });
      const res = await api<RangeMatrix>(`/metrics/query_range?${params}`);
      return res.data.result;
    },
    refetchInterval: 30_000,
  });
}
