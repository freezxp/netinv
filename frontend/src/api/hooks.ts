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
      setTimeout(() => qc.invalidateQueries({ queryKey: ["device", id] }), 4000),
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
export function trafficExpr(deviceID: string, ifIndex: number | string) {
  const sel = `{device_id="${deviceID}",if_index="${ifIndex}"}`;
  return (
    `label_set(rate(netinv_if_in_octets_total${sel}[5m]) * 8, "dir", "in")` +
    ` or label_set(rate(netinv_if_out_octets_total${sel}[5m]) * 8, "dir", "out")`
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
    .map(([name, metric]) => `label_set(${metric}${selector}, "series", "${name}")`)
    .join(" or ");
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
