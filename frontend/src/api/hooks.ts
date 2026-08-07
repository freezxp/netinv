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
