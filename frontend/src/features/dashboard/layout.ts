// Dashboard layout: which panels appear, in what order, and how each is
// configured. Persisted per user through /users/me/preferences so a layout
// follows the operator rather than the browser.
//
// The dashboard was a fixed sequence of panels chosen at build time, which is
// the wrong default for a NOC: what matters on screen differs between someone
// watching capacity, someone watching a specific site, and someone who wants
// the weathermap up all day.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";

export type PanelKind =
  | "status"
  | "alerts"
  | "bandwidth_in"
  | "bandwidth_out"
  | "latency"
  | "topn"
  | "heatmap"
  | "watchlist"
  | "weathermap"
  | "metric"
  | "flow";

export interface Panel {
  /** Stable within a layout; lets two panels of the same kind coexist. */
  id: string;
  kind: PanelKind;
  /** Overrides the default heading. */
  title?: string;
  /** Full width rather than half. */
  wide?: boolean;
  /** weathermap: which map to embed. */
  mapID?: string;
  /** metric: the series to chart, and an optional label filter. */
  metric?: string;
  /** metric: e.g. `site="FN"` — appended into the selector as written. */
  filter?: string;
  /** metric: aggregate rather than one line per series. */
  groupBy?: string;
  /** metric: treat as a counter and rate() it. */
  rate?: boolean;
  /** flow: which ranking to show. */
  flowDimension?: "talker" | "conversation" | "application";
  /** flow: a single exporter, or empty for the whole fleet. */
  exporter?: string;
  /** flow: how many rows to list. */
  topN?: number;
}

export interface DashboardLayout {
  panels: Panel[];
}

// What a fresh install shows: the panels the dashboard had before it became
// customisable, in the same order. Someone who never opens the editor should
// not notice that an editor exists.
export const DEFAULT_LAYOUT: DashboardLayout = {
  panels: [
    { id: "status", kind: "status", wide: true },
    { id: "alerts", kind: "alerts", wide: true },
    { id: "bw-in", kind: "bandwidth_in" },
    { id: "bw-out", kind: "bandwidth_out" },
    { id: "latency", kind: "latency", wide: true },
    { id: "topn", kind: "topn", wide: true },
    { id: "heatmap", kind: "heatmap", wide: true },
    { id: "watchlist", kind: "watchlist", wide: true },
  ],
};

export const PANEL_LABELS: Record<PanelKind, string> = {
  status: "Status summary",
  alerts: "Active alerts",
  bandwidth_in: "Bandwidth in, by site",
  bandwidth_out: "Bandwidth out, by site",
  latency: "Latency (ICMP RTT)",
  topn: "Top-N lists",
  heatmap: "Device health heatmap",
  watchlist: "Capacity watchlist",
  weathermap: "Weathermap",
  metric: "Custom metric",
  flow: "Top flow (talkers/apps)",
};

/** Panels that can appear more than once, because each instance is configured. */
// flow repeats because the three dimensions answer different questions, and
// wanting talkers and applications side by side is the normal case rather than
// an edge one.
export const REPEATABLE: PanelKind[] = ["weathermap", "metric", "flow"];

interface Prefs {
  dashboard?: DashboardLayout;
  [k: string]: unknown;
}

function valid(l: unknown): l is DashboardLayout {
  if (!l || typeof l !== "object") return false;
  const panels = (l as DashboardLayout).panels;
  return Array.isArray(panels) && panels.every((p) => p && p.id && p.kind);
}

export function useDashboardLayout() {
  const qc = useQueryClient();
  const prefs = useQuery({
    queryKey: ["prefs"],
    queryFn: () => api<Prefs>("/users/me/preferences"),
    staleTime: 5 * 60_000,
  });

  const save = useMutation({
    mutationFn: (layout: DashboardLayout) =>
      api<Prefs>("/users/me/preferences", {
        method: "PUT",
        // Merged, not replaced: preferences are shared with whatever else the
        // product stores there, and a dashboard save must not drop a theme.
        body: JSON.stringify({ ...(prefs.data ?? {}), dashboard: layout }),
      }),
    onSuccess: (saved) => qc.setQueryData(["prefs"], saved),
  });

  // A stored layout from an older version can be missing panels or carry ones
  // that no longer exist. Falling back whole is better than rendering a
  // half-broken dashboard the operator cannot repair without dev tools.
  const stored = prefs.data?.dashboard;
  const layout = valid(stored)
    ? { panels: stored.panels.filter((p) => p.kind in PANEL_LABELS) }
    : DEFAULT_LAYOUT;

  return { layout, save, loading: prefs.isLoading };
}

export function move(panels: Panel[], id: string, delta: number): Panel[] {
  const i = panels.findIndex((p) => p.id === id);
  const j = i + delta;
  if (i < 0 || j < 0 || j >= panels.length) return panels;
  const next = [...panels];
  [next[i], next[j]] = [next[j], next[i]];
  return next;
}

let seq = 0;
export function newPanelID(kind: PanelKind): string {
  seq += 1;
  return `${kind}-${Date.now().toString(36)}-${seq}`;
}
