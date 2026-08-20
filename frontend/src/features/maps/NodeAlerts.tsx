// Active alerts for a weathermap node.
//
// A node's colour says something is wrong; it cannot say what. Getting from
// "that box is red" to the reason meant leaving the map for the alerts page and
// finding the device by name, which is the one thing a weathermap exists to
// save you. This shows the alerts against the device the node is bound to.
//
// Pinned and interactive for the same reasons as the link graph: an alert is
// read, not glanced at, and the card carries a link through to the device.
import { Link } from "react-router-dom";
import type { Alert } from "../../api/types";
import { SeverityPill } from "../../components/ui";
import { formatAlertInterface, formatDuration } from "../../lib/format";
import type { MapDefinition } from "./api";

const WIDTH = 340;

export function NodeAlerts({
  def,
  nodeID,
  alerts,
  at,
  onPointerEnter,
  onPointerLeave,
  onClose,
}: {
  def: MapDefinition;
  nodeID: string;
  alerts: Alert[];
  at: { x: number; y: number };
  onPointerEnter?: () => void;
  onPointerLeave?: () => void;
  onClose?: () => void;
}) {
  const node = (def.nodes ?? []).find((n) => n.id === nodeID);
  const pad = 12;
  const left = Math.min(at.x + 16, window.innerWidth - WIDTH - pad);
  const top = Math.min(at.y + 16, window.innerHeight - 240 - pad);

  return (
    <div
      onMouseEnter={onPointerEnter}
      onMouseLeave={onPointerLeave}
      className="fixed z-40 rounded-lg border border-slate-200 bg-white p-2 shadow-lg dark:border-slate-700 dark:bg-slate-900"
      style={{ left: Math.max(pad, left), top: Math.max(pad, top), width: WIDTH }}
    >
      <div className="mb-1 flex items-baseline justify-between gap-2 px-1">
        <span className="text-xs font-medium">{node?.label ?? "Device"}</span>
        <span className="flex items-center gap-2 text-[10px] text-slate-500">
          {alerts.length} active
          <button
            type="button"
            onClick={onClose}
            aria-label="Close alerts"
            className="rounded px-1 leading-none text-slate-400 hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-200"
          >
            ✕
          </button>
        </span>
      </div>
      {/* Capped and scrolled: a device mid-incident can carry dozens, and a
          card taller than the window cannot be closed by leaving it. */}
      <div className="max-h-56 overflow-y-auto">
        {alerts.map((a) => (
          <div
            key={a.id}
            className="flex flex-wrap items-center gap-x-2 gap-y-0.5 border-b border-slate-100 px-1 py-1 last:border-0 dark:border-slate-800/60"
          >
            <SeverityPill severity={a.severity} />
            <span className="text-xs font-medium">{a.rule.name}</span>
            <span className="text-[10px] text-slate-500">
              {[formatAlertInterface(a.labels), formatDuration(a.duration_s)]
                .filter(Boolean)
                .join(" · ")}
              {a.state === "acknowledged" && " · acked"}
            </span>
          </div>
        ))}
      </div>
      {node?.device_id && (
        <Link
          to={`/devices/${node.device_id}`}
          className="mt-1 block px-1 text-[10px] text-sky-500 hover:underline"
        >
          Open device →
        </Link>
      )}
    </div>
  );
}
