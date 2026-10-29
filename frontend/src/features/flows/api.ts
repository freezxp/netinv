// Raw flow explorer API (doc 09 §7.2, ADR-026). Feature-local: nothing
// outside the Flows page reads these endpoints.
import { useQuery } from "@tanstack/react-query";
import { api } from "../../api/client";

export interface FlowField {
  name: string;
  label: string;
  kind: string;
  rollup: boolean;
  group: boolean;
}

export interface FlowStoreStatus {
  rows: number;
  bytes_on_disk: number;
  oldest?: string;
  newest?: string;
  rollup_rows: number;
  written: number;
  dropped: number;
  queued: number;
  geo_asn_ranges: number;
  geo_country_ranges: number;
  reported_at?: string;
}

export interface FlowStatus {
  enabled: boolean;
  raw_days: number;
  rollup_days: number;
  fields: FlowField[];
  store?: FlowStoreStatus;
  error?: string;
}

export function useFlowStatus() {
  return useQuery({
    queryKey: ["flow-explore-status"],
    queryFn: () => api<FlowStatus>("/flow/explore/status"),
    refetchInterval: 60_000,
  });
}

export interface FlowFilter {
  field: string;
  negate?: boolean;
  values: string[];
}

export interface ExploreBody {
  from: string;
  to: string;
  filters: FlowFilter[];
  group_by: string[];
  metric: "bytes" | "packets" | "flows";
  limit: number;
  devices?: string[];
  order_by?: "time" | "bytes";
}

export interface FlowGroup {
  keys: string[];
  bytes: number;
  packets: number;
  flows: number;
}

export interface FlowLabels {
  exporters: Record<string, { device_id: string; device_name: string }>;
  interfaces: Record<string, string>;
}

export interface ExploreResult {
  result: {
    table: "flows" | "flows_1h";
    groups: FlowGroup[];
    total: FlowGroup;
    series: {
      step: number;
      times: number[];
      total: number[];
      lines: number[][] | null;
    };
  };
  labels: FlowLabels;
}

export type FlowRecord = Record<string, string | number>;

export function useFlowExplore(body: ExploreBody | null) {
  return useQuery({
    queryKey: ["flow-explore", body],
    queryFn: () =>
      api<ExploreResult>("/flow/explore", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    enabled: body !== null,
    placeholderData: (prev) => prev,
    retry: false,
  });
}

export function useFlowRecords(body: ExploreBody | null) {
  return useQuery({
    queryKey: ["flow-records", body],
    queryFn: () =>
      api<{ records: FlowRecord[]; labels: FlowLabels }>("/flow/records", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    enabled: body !== null,
    placeholderData: (prev) => prev,
    retry: false,
  });
}
