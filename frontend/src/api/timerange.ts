// Shared time range for every graph in the portal.
//
// One selection applies across pages and survives a reload, because the
// question an operator is holding in their head — "what did last week look
// like?" — does not change when they click from the dashboard into a device.
// Having each chart carry its own hard-coded window meant the dashboard showed
// 24h, a device's traffic chart 6h and its health charts 24h, with nothing on
// screen saying so.
//
// The presets are Cacti's graph timespans, in Cacti's order. NetInv is a
// successor to Cacti + Weathermap and the people evaluating it have twenty
// years of muscle memory for that list; inventing a shorter one buys nothing
// and costs familiarity.
import { create } from "zustand";

export type RangeKey =
  | "30m"
  | "1h"
  | "2h"
  | "4h"
  | "6h"
  | "12h"
  | "1d"
  | "2d"
  | "3d"
  | "4d"
  | "1w"
  | "2w"
  | "1mo"
  | "2mo"
  | "3mo"
  | "4mo"
  | "6mo"
  | "1y"
  | "2y";

export interface TimeRange {
  key: RangeKey;
  /** Cacti's wording, so the dropdown reads the way operators expect. */
  label: string;
  /** Compact form for chart titles, where "Last Half Hour" is too long. */
  short: string;
  hours: number;
  /** Query resolution. Chosen to land every span near 150–400 points: enough
   * to see shape, few enough that VictoriaMetrics and uPlot stay quick.
   * Never below the 60s poll interval, which is the finest real resolution
   * there is — a smaller step only interpolates. */
  stepS: number;
}

const H = 1;
const D = 24;

export const TIME_RANGES: readonly TimeRange[] = [
  { key: "30m", label: "Last Half Hour", short: "30m", hours: 0.5, stepS: 60 },
  { key: "1h", label: "Last Hour", short: "1h", hours: 1 * H, stepS: 60 },
  { key: "2h", label: "Last 2 Hours", short: "2h", hours: 2 * H, stepS: 60 },
  { key: "4h", label: "Last 4 Hours", short: "4h", hours: 4 * H, stepS: 60 },
  { key: "6h", label: "Last 6 Hours", short: "6h", hours: 6 * H, stepS: 120 },
  { key: "12h", label: "Last 12 Hours", short: "12h", hours: 12 * H, stepS: 180 },
  { key: "1d", label: "Last Day", short: "1d", hours: 1 * D, stepS: 300 },
  { key: "2d", label: "Last 2 Days", short: "2d", hours: 2 * D, stepS: 600 },
  { key: "3d", label: "Last 3 Days", short: "3d", hours: 3 * D, stepS: 900 },
  { key: "4d", label: "Last 4 Days", short: "4d", hours: 4 * D, stepS: 1200 },
  { key: "1w", label: "Last Week", short: "1w", hours: 7 * D, stepS: 1800 },
  { key: "2w", label: "Last 2 Weeks", short: "2w", hours: 14 * D, stepS: 3600 },
  { key: "1mo", label: "Last Month", short: "1mo", hours: 30 * D, stepS: 7200 },
  { key: "2mo", label: "Last 2 Months", short: "2mo", hours: 60 * D, stepS: 14400 },
  { key: "3mo", label: "Last 3 Months", short: "3mo", hours: 90 * D, stepS: 21600 },
  { key: "4mo", label: "Last 4 Months", short: "4mo", hours: 120 * D, stepS: 28800 },
  { key: "6mo", label: "Last 6 Months", short: "6mo", hours: 180 * D, stepS: 43200 },
  { key: "1y", label: "Last Year", short: "1y", hours: 365 * D, stepS: 86400 },
  { key: "2y", label: "Last 2 Years", short: "2y", hours: 730 * D, stepS: 172800 },
];

/** Matches Cacti's own default of "Last Day". */
export const DEFAULT_RANGE: RangeKey = "1d";

/**
 * Spans longer than this cannot be filled by the default deployment, whose
 * VictoriaMetrics retention is 90 days (`-retentionPeriod=90d`). They are
 * offered anyway — retention is a deploy-time flag the browser cannot read,
 * an operator may well have raised it, and a graph that stops early is
 * self-explanatory in a way that a missing menu entry is not.
 */
export const DEFAULT_RETENTION_HOURS = 90 * D;

export function rangeFor(key: RangeKey): TimeRange {
  return TIME_RANGES.find((r) => r.key === key) ?? TIME_RANGES[6];
}

// The shortest poll interval any profile uses. Rate windows are derived from
// it so a counter that advances once per minute still produces a rate.
const POLL_INTERVAL_S = 60;

// rateWindow returns the lookback for rate() at a given resolution.
//
// A fixed [5m] breaks at both ends of the selector. Over a year the step is a
// day, so rate(x[5m]) measures five minutes out of every twenty-four hours and
// reports that as the whole day — traffic looks like noise and the peaks are
// wrong. Over half an hour a window far larger than the step smears real
// spikes away. The window therefore tracks the step, never dropping below four
// poll intervals, which is what keeps a rate defined when a device is polled
// slowly or misses a sample.
//
// This is the same reasoning as Grafana's $__rate_interval; the name is
// avoided because nothing here is Grafana.
export function rateWindow(stepS: number): string {
  return `${Math.max(4 * POLL_INTERVAL_S, stepS + POLL_INTERVAL_S)}s`;
}

const STORAGE_KEY = "netinv.timerange";

// Keys used before the presets became Cacti's. Mapped rather than discarded so
// an existing selection survives the upgrade instead of silently resetting.
const RENAMED: Record<string, RangeKey> = { "24h": "1d", "7d": "1w" };

function initial(): RangeKey {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved) {
      if (RENAMED[saved]) return RENAMED[saved];
      if (TIME_RANGES.some((r) => r.key === saved)) return saved as RangeKey;
    }
  } catch {
    // Private browsing or a blocked storage partition. A default is fine;
    // failing to render the dashboard over a preference is not.
  }
  return DEFAULT_RANGE;
}

interface TimeRangeState {
  key: RangeKey;
  range: TimeRange;
  setRange: (key: RangeKey) => void;
}

export const useTimeRangeStore = create<TimeRangeState>((set) => ({
  key: initial(),
  range: rangeFor(initial()),
  setRange: (key) => {
    try {
      localStorage.setItem(STORAGE_KEY, key);
    } catch {
      // Same as above — the selection still applies for this session.
    }
    set({ key, range: rangeFor(key) });
  },
}));

/** The selected range. Use this rather than reading the store's key directly. */
export function useTimeRange(): TimeRange {
  return useTimeRangeStore((s) => s.range);
}
