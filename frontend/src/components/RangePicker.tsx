// Graph time range selector.
//
// A dropdown rather than the segmented buttons this started as: the presets
// are now Cacti's nineteen timespans, which is more than a button row can
// carry, and a dropdown is what Cacti itself uses — so the control looks the
// way the people migrating from it expect.
//
// Spans beyond the default 90-day retention are grouped separately rather than
// hidden. Retention is a deploy-time flag the browser cannot read, so removing
// them would be guessing on the operator's behalf; a graph that stops early
// explains itself, a missing menu entry does not.
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

const within = TIME_RANGES.filter((r) => r.hours <= DEFAULT_RETENTION_HOURS);
const beyond = TIME_RANGES.filter((r) => r.hours > DEFAULT_RETENTION_HOURS);

export function RangePicker({ className, ariaLabel = "Time range" }: Props) {
  const key = useTimeRangeStore((s) => s.key);
  const setRange = useTimeRangeStore((s) => s.setRange);

  return (
    <Select
      aria-label={ariaLabel}
      className={className}
      value={key}
      onChange={(e) => setRange(e.target.value as RangeKey)}
    >
      {within.map((r) => (
        <option key={r.key} value={r.key}>
          {r.label}
        </option>
      ))}
      <optgroup label="Beyond default retention (90d)">
        {beyond.map((r) => (
          <option key={r.key} value={r.key}>
            {r.label}
          </option>
        ))}
      </optgroup>
    </Select>
  );
}
