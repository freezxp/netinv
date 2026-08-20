// Link graph for a weathermap — Cacti's habit of showing the RRD graph by the
// cursor, so you can tell a steady 80% from a spike without leaving the map.
//
// It is pinned rather than dragged along with the pointer, and it accepts
// pointer events. Both matter for the same reason: a card that follows the
// cursor and ignores clicks can be looked at but not used, and reading a value
// off a graph means putting the cursor on the graph. uPlot updates its legend
// from the cursor position, so hovering the plot reads out in/out at that
// instant.
import { trafficExpr, useQueryRange } from "../../api/hooks";
import { TimeSeries } from "../../components/TimeSeries";
import { formatBps } from "../../lib/format";
import { rateWindow, useTimeRange } from "../../api/timerange";
import { useMetricsLimits } from "../../api/hooks";
import type { LiveData, MapDefinition } from "./api";

const WIDTH = 400;
const HEIGHT = 200;

export function LinkGraph({
  def,
  live,
  linkID,
  at,
  onPointerEnter,
  onPointerLeave,
  onClose,
}: {
  def: MapDefinition;
  live?: LiveData;
  linkID: string;
  at: { x: number; y: number };
  onPointerEnter?: () => void;
  onPointerLeave?: () => void;
  onClose?: () => void;
}) {
  const link = (def.links ?? []).find((l) => l.id === linkID);
  const lv = live?.links?.find((l) => l.id === linkID);
  const a = link?.a_endpoint;
  const nodeLabel = (id?: string) =>
    (def.nodes ?? []).find((n) => n.id === id)?.label ?? "?";

  // Same expression and the same shared range as the device page charts, so
  // the two always agree. The card carries no picker of its own; it follows
  // whatever range is selected on the page chrome.
  const range = useTimeRange();
  const pollS = useMetricsLimits().data?.poll_interval_s ?? 0;
  const traffic = useQueryRange(
    a
      ? trafficExpr(a.device_id, a.if_index, rateWindow(range.stepS, pollS))
      : `vector(0)`,
    range.hours,
    range.stepS,
  );

  // Keep the card on screen when the cursor is near an edge of the window.
  const pad = 12;
  const left = Math.min(at.x + 16, window.innerWidth - WIDTH - pad);
  const top = Math.min(at.y + 16, window.innerHeight - HEIGHT - pad);

  return (
    <div
      onMouseEnter={onPointerEnter}
      onMouseLeave={onPointerLeave}
      className="fixed z-40 rounded-lg border border-slate-200 bg-white p-2 shadow-lg dark:border-slate-700 dark:bg-slate-900"
      style={{
        left: Math.max(pad, left),
        top: Math.max(pad, top),
        width: WIDTH,
      }}
    >
      <div className="mb-1 flex items-baseline justify-between gap-2 px-1">
        <span className="text-xs font-medium">
          {nodeLabel(link?.from)} ⇄ {nodeLabel(link?.to)}
        </span>
        <span className="flex items-center gap-2 text-[10px] text-slate-500">
          last {range.short}
          <button
            type="button"
            onClick={onClose}
            aria-label="Close link graph"
            className="rounded px-1 leading-none text-slate-400 hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-200"
          >
            ✕
          </button>
        </span>
      </div>
      {!a ? (
        <div className="px-1 pb-2 text-xs text-slate-500">
          This link has no interface bound, so there is nothing to graph.
        </div>
      ) : (
        <>
          <TimeSeries
            result={traffic.data ?? []}
            height={120}
            windowHours={range.hours}
            format={formatBps}
            label={(m) => m.dir ?? "in"}
          />
          <div className="mt-1 flex justify-between px-1 text-[10px] text-slate-500">
            <span>
              now {formatBps(lv?.in_bps ?? 0)} in /{" "}
              {formatBps(lv?.out_bps ?? 0)} out
            </span>
            <span>
              {lv && lv.capacity_bps > 0
                ? `of ${formatBps(lv.capacity_bps)}`
                : "no capacity set"}
            </span>
          </div>
        </>
      )}
    </div>
  );
}
