// Shared time range for every graph in the portal.
//
// One selection applies across pages and survives a reload, because the
// question an operator is holding in their head — "what did last week look
// like?" — does not change when they click from the dashboard into a device.
// Having each chart carry its own hard-coded window meant the dashboard showed
// 24h, a device's traffic chart 6h and its health charts 24h, with nothing on
// screen saying so.
import { create } from "zustand";

export type RangeKey = "1h" | "6h" | "24h" | "7d";

export interface TimeRange {
  key: RangeKey;
  label: string;
  hours: number;
  /** Query resolution. Chosen to land every range near 150–350 points: enough
   * to see shape, few enough that VictoriaMetrics and uPlot stay quick. */
  stepS: number;
}

export const TIME_RANGES: readonly TimeRange[] = [
  { key: "1h", label: "1h", hours: 1, stepS: 30 }, // 120 points
  { key: "6h", label: "6h", hours: 6, stepS: 120 }, // 180
  { key: "24h", label: "24h", hours: 24, stepS: 300 }, // 288
  { key: "7d", label: "7d", hours: 168, stepS: 1800 }, // 336
];

export const DEFAULT_RANGE: RangeKey = "24h";

export function rangeFor(key: RangeKey): TimeRange {
  return TIME_RANGES.find((r) => r.key === key) ?? TIME_RANGES[2];
}

// The shortest poll interval any profile uses. Rate windows are derived from
// it so a counter that advances once per minute still produces a rate.
const POLL_INTERVAL_S = 60;

// rateWindow returns the lookback for rate() at a given resolution.
//
// A fixed [5m] breaks at both ends of the range selector. Over 7 days the step
// is 30 minutes, so rate(x[5m]) measures five minutes out of every thirty and
// reports that as the whole bucket — traffic looks violently spiky and the
// peaks are wrong. Over 1 hour a window far larger than the step smears real
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

function initial(): RangeKey {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved && TIME_RANGES.some((r) => r.key === saved)) {
      return saved as RangeKey;
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
