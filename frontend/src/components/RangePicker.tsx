// Time range selector. A segmented control rather than a dropdown: there are
// four options, they are the most-changed control on the page, and a dropdown
// would cost two clicks to do what one should.
import {
  TIME_RANGES,
  useTimeRangeStore,
  type RangeKey,
} from "../api/timerange";
import { cx } from "./ui";

interface Props {
  className?: string;
  /** Screen-reader label; defaults to something generic. */
  ariaLabel?: string;
}

export function RangePicker({ className, ariaLabel = "Time range" }: Props) {
  const key = useTimeRangeStore((s) => s.key);
  const setRange = useTimeRangeStore((s) => s.setRange);

  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className={cx(
        "inline-flex shrink-0 overflow-hidden rounded-md border border-slate-300",
        "dark:border-slate-700",
        className,
      )}
    >
      {TIME_RANGES.map((r) => {
        const active = r.key === key;
        return (
          <button
            key={r.key}
            type="button"
            onClick={() => setRange(r.key as RangeKey)}
            aria-pressed={active}
            // The label is "1h", which reads as a duration but not as a
            // direction; the title says which way it points.
            title={`Last ${r.label}`}
            className={cx(
              "px-2.5 py-1.5 text-sm tabular-nums transition-colors",
              "border-r border-slate-300 last:border-r-0 dark:border-slate-700",
              active
                ? "bg-sky-600 font-medium text-white"
                : "bg-white text-slate-600 hover:bg-slate-100 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800",
            )}
          >
            {r.label}
          </button>
        );
      })}
    </div>
  );
}
