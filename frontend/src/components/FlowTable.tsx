// The ranked flow table, shared by the device Flow tab and the dashboard flow
// panel. Shared rather than copied because the column semantics are the part
// that is easy to get wrong: two of these columns are byte totals and one is a
// rate averaged over the whole selected range, and a second copy would drift
// from that labelling the first time someone edited one of them.
import type { FlowDimension, FlowRow } from "../api/hooks";
import { formatBps, formatBytes } from "../lib/format";
import { EmptyState } from "./ui";

export const DIMENSION_NOUN: Record<FlowDimension, string> = {
  talker: "Host",
  conversation: "Conversation",
  application: "Application",
};

export function FlowTable({
  rows,
  dimension,
  rangeShort,
  rangeS,
  limit,
}: {
  rows: FlowRow[];
  dimension: FlowDimension;
  /** e.g. "1d" — names the window the average is taken over. */
  rangeShort: string;
  rangeS: number;
  limit?: number;
}) {
  if (rows.length === 0) {
    return (
      <EmptyState>
        Nothing in this window. Flow is kept per interval, so a range with no
        exported traffic is simply empty.
      </EmptyState>
    );
  }
  // Share is of what was kept, not of everything that crossed the link — the
  // top-N cut happens before storage, so the denominator excludes whatever
  // fell outside it.
  const grandTotal = rows.reduce((sum, r) => sum + r.bytes, 0);
  const shown = limit ? rows.slice(0, limit) : rows;

  return (
    <table className="w-full text-sm">
      {/* Headers are not decoration here. "434 MB … 40.2 Kbps" on one row is
          unreadable without being told the rate is averaged across the whole
          selected range — far below the peak on any chart beside it whenever
          traffic covers only part of that window. */}
      <thead>
        <tr className="text-xs uppercase text-slate-500 dark:text-slate-400">
          <th className="pb-2 text-left font-medium">
            {DIMENSION_NOUN[dimension]}
          </th>
          <th />
          <th className="pb-2 pl-2 text-right font-medium">Total</th>
          <th className="pb-2 pl-3 text-right font-medium">Share</th>
          <th className="pb-2 pl-3 text-right font-medium whitespace-nowrap">
            Avg over {rangeShort}
          </th>
        </tr>
      </thead>
      <tbody>
        {shown.map((r) => {
          const share = grandTotal > 0 ? r.bytes / grandTotal : 0;
          return (
            <tr
              key={r.value}
              className="border-b border-slate-100 last:border-0 dark:border-slate-800"
            >
              <td className="mono py-1.5 pr-3 whitespace-nowrap">{r.value}</td>
              <td className="w-full px-2">
                <div className="h-2 w-full rounded bg-slate-100 dark:bg-slate-800">
                  <div
                    className="h-2 rounded bg-sky-500"
                    style={{ width: `${Math.max(share * 100, 1)}%` }}
                  />
                </div>
              </td>
              <td className="py-1.5 pl-2 text-right tabular-nums whitespace-nowrap">
                {formatBytes(r.bytes)}
              </td>
              <td className="py-1.5 pl-3 text-right tabular-nums whitespace-nowrap text-slate-500">
                {(share * 100).toFixed(1)}%
              </td>
              <td className="py-1.5 pl-3 text-right tabular-nums whitespace-nowrap text-slate-500">
                {formatBps((r.bytes * 8) / Math.max(rangeS, 1))}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
