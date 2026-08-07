import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";

export interface MapNode {
  id: string;
  kind: "device" | "site" | "cloud" | "label";
  device_id?: string;
  site_id?: string;
  text?: string;
  label?: string;
  x: number;
  y: number;
}

export interface MapEndpoint {
  device_id: string;
  if_index: number;
}

export interface MapLink {
  id: string;
  from: string;
  to: string;
  a_endpoint?: MapEndpoint;
  b_endpoint?: MapEndpoint;
  bandwidth_bps?: number;
}

export interface MapDefinition {
  schema: string;
  options?: Record<string, unknown>;
  nodes: MapNode[];
  links: MapLink[];
}

export interface MapMeta {
  id: string;
  name: string;
  published_rev: number;
  draft_rev: number;
  updated_at: string;
}

export interface LiveData {
  as_of: string;
  nodes: Array<{ id: string; state: string }>;
  links: Array<{
    id: string;
    in_bps: number;
    out_bps: number;
    util_in: number;
    util_out: number;
    state: string;
  }>;
}

export function useMaps() {
  return useQuery({
    queryKey: ["maps"],
    queryFn: () => api<{ data: MapMeta[] }>("/maps"),
  });
}

export function useCreateMap() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      api<MapMeta>("/maps", { method: "POST", body: JSON.stringify({ name }) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["maps"] }),
  });
}

export function useMapDef(id: string, rev: "draft" | "published") {
  return useQuery({
    queryKey: ["map", id, rev],
    queryFn: () =>
      api<{ rev: number; definition: MapDefinition }>(`/maps/${id}?rev=${rev}`),
  });
}

export function useSaveDraft(id: string) {
  return useMutation({
    mutationFn: (def: MapDefinition) =>
      api<void>(`/maps/${id}/draft`, {
        method: "PUT",
        body: JSON.stringify(def),
      }),
  });
}

export function usePublish(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api<{ published_rev: number }>(`/maps/${id}/publish`, { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["maps"] });
      qc.invalidateQueries({ queryKey: ["map", id] });
    },
  });
}

export function useMapLive(id: string, enabled: boolean) {
  return useQuery({
    queryKey: ["map", id, "live"],
    queryFn: () => api<LiveData>(`/maps/${id}/live`),
    enabled,
    refetchInterval: 15_000, // FR-MAP-05: ≤30s staleness
  });
}

export interface Suggestion {
  a_device_id: string;
  a_device: string;
  a_if_index: number;
  b_device_id: string;
  b_device: string;
  b_sysname: string;
  b_port: string;
}

export function useSuggestions(mapID: string) {
  return useQuery({
    queryKey: ["map", mapID, "suggestions"],
    queryFn: () => api<{ data: Suggestion[] }>(`/maps/${mapID}/suggestions`),
  });
}

// Classic weathermap utilization scale (FR-MAP-03).
const scale: Array<[number, string]> = [
  [1, "#8b5cf6"],
  [10, "#3b82f6"],
  [25, "#22c55e"],
  [40, "#84cc16"],
  [55, "#eab308"],
  [70, "#f59e0b"],
  [85, "#f97316"],
  [100, "#ef4444"],
];

export function utilColor(util: number, state: string): string {
  if (state === "down") return "#ef4444";
  if (state === "nodata") return "#64748b";
  for (const [max, color] of scale) {
    if (util <= max) return color;
  }
  return "#dc2626";
}

export const utilLegend = scale;
