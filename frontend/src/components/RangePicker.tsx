// Graph time range selector.
//
// A dropdown rather than the segmented buttons this started as: the presets
// are now Cacti's nineteen timespans, which is more than a button row can
// carry, and a dropdown is what Cacti itself uses — so the control looks the
// way the people migrating from it expect.
import { useMetricsLimits } from "../api/hooks";
import {
  DEFAULT_RETENTION_HOURS,
  TIME_RANGES,
  useTimeRangeStore,
  type RangeKey,
} from "../api/timerange";
import { Select } from "./ui";

interface Props {
  className?: string;
  /** Screen-reader label; defaults to something generic. */
  ariaLabel?: string;
}

export function RangePicker({ className, ariaLabel = "Time range" }: Props) {
  const key = useTimeRangeStore((s) => s.key);
  const setRange = useTimeRangeStore((s) => s.setRange);

  // The API rejects a range past its ceiling outright rather than clamping it,
  // so anything beyond this produces an error instead of a shorter graph. The
  // ceiling follows the deployment's retention and cannot be known at build
  // time — offering the whole Cacti list regardless is how "Last Year" came to
  // return "range exceeds 90 days" instead of a chart.
  const limits = useMetricsLimits();
  const maxHours = limits.data
    ? limits.data.max_range_s / 3600
    : DEFAULT_RETENTION_HOURS;

  const usable = TIME_RANGES.filter((r) => r.hours <= maxHours);
  const beyond = TIME_RANGES.filter((r) => r.hours > maxHours);

  return (
    <Select
      aria-label={ariaLabel}
      className={className}
      value={key}
      onChange={(e) => setRange(e.target.value as RangeKey)}
    >
      {usable.map((r) => (
        <option key={r.key} value={r.key}>
          {r.label}
        </option>
      ))}
      {beyond.length > 0 && (
        // Shown but disabled: an operator who wonders where "Last Year" went
        // is worse off than one who can see it and why it is unavailable.
        // Raising NETINV_VM_RETENTION re-enables them.
        <optgroup label={`Beyond retention (${Math.round(maxHours / 24)}d)`}>
          {beyond.map((r) => (
            <option key={r.key} value={r.key} disabled>
              {r.label}
            </option>
          ))}
        </optgroup>
      )}
    </Select>
  );
}
