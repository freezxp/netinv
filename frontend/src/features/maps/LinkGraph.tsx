// Hover graph for a weathermap link — Cacti's habit of showing the RRD graph
// under the cursor, so you can tell a steady 80% from a spike without leaving
// the map.
import { trafficExpr, useQueryRange } from "../../api/hooks";
import { TimeSeries } from "../../components/TimeSeries";
import { formatBps } from "../../lib/format";
import type { LiveData, MapDefinition } from "./api";

const WIDTH = 400;
const HEIGHT = 200;

export function LinkGraph({
  def,
  live,
  linkID,
  at,
}: {
  def: MapDefinition;
  live?: LiveData;
  linkID: string;
  at: { x: number; y: number };
}) {
  const link = (def.links ?? []).find((l) => l.id === linkID);
  const lv = live?.links?.find((l) => l.id === linkID);
  const a = link?.a_endpoint;
  const nodeLabel = (id?: string) =>
    (def.nodes ?? []).find((n) => n.id === id)?.label ?? "?";

  // Same expression the device page charts, so the two always agree.
  const traffic = useQueryRange(
    a ? trafficExpr(a.device_id, a.if_index) : `vector(0)`,
    6,
    120,
  );

  // Keep the card on screen when the cursor is near an edge of the window.
  const pad = 12;
  const left = Math.min(at.x + 16, window.innerWidth - WIDTH - pad);
  const top = Math.min(at.y + 16, window.innerHeight - HEIGHT - pad);

  return (
    <div
      // pointer-events-none: the card follows the hovered link, and must never
      // become the hover target itself or it would flicker as it appears.
      className="pointer-events-none fixed z-40 rounded-lg border border-slate-200 bg-white p-2 shadow-lg dark:border-slate-700 dark:bg-slate-900"
      style={{ left: Math.max(pad, left), top: Math.max(pad, top), width: WIDTH }}
    >
      <div className="mb-1 flex items-baseline justify-between gap-2 px-1">
        <span className="text-xs font-medium">
          {nodeLabel(link?.from)} ⇄ {nodeLabel(link?.to)}
        </span>
        <span className="text-[10px] text-slate-500">last 6h</span>
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
            windowHours={6}
            format={formatBps}
            label={(m) => m.dir ?? "in"}
          />
          <div className="mt-1 flex justify-between px-1 text-[10px] text-slate-500">
            <span>
              now {formatBps(lv?.in_bps ?? 0)} in / {formatBps(lv?.out_bps ?? 0)} out
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
