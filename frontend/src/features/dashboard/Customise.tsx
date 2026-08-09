// Dashboard editor: add, remove, reorder and configure panels.
//
// Reordering is buttons rather than drag-and-drop. Drag is nicer with a mouse
// and unusable on the phone layout NFR-60 requires, and a NOC dashboard is
// arranged once and then left alone — the wrong place to spend the complexity.
import { useState } from "react";
import { Button, Card, Input, Select, cx } from "../../components/ui";
import { useMaps, useMetricNames } from "./CustomPanels";
import {
  PANEL_LABELS,
  REPEATABLE,
  move,
  newPanelID,
  type DashboardLayout,
  type Panel,
  type PanelKind,
} from "./layout";

const ALL_KINDS = Object.keys(PANEL_LABELS) as PanelKind[];

export function Customise({
  layout,
  onChange,
  onClose,
  saving,
}: {
  layout: DashboardLayout;
  onChange: (l: DashboardLayout) => void;
  onClose: () => void;
  saving: boolean;
}) {
  const maps = useMaps();
  const metrics = useMetricNames();
  const [adding, setAdding] = useState<PanelKind>("weathermap");

  const panels = layout.panels;
  const set = (next: Panel[]) => onChange({ panels: next });
  const patch = (id: string, p: Partial<Panel>) =>
    set(panels.map((x) => (x.id === id ? { ...x, ...p } : x)));

  // A non-repeatable panel already on the dashboard would render twice with
  // identical content, which looks like a bug rather than a choice.
  const available = ALL_KINDS.filter(
    (k) => REPEATABLE.includes(k) || !panels.some((p) => p.kind === k),
  );

  return (
    <Card title="Customise dashboard">
      <div className="flex flex-col gap-2">
        {panels.map((p, i) => (
          <div
            key={p.id}
            className="rounded-lg border border-slate-200 p-2 dark:border-slate-800"
          >
            <div className="flex flex-wrap items-center gap-2">
              <span className="flex-1 text-sm font-medium">
                {PANEL_LABELS[p.kind]}
              </span>
              <Button
                variant="ghost"
                aria-label={`Move ${PANEL_LABELS[p.kind]} up`}
                disabled={i === 0}
                onClick={() => set(move(panels, p.id, -1))}
              >
                ↑
              </Button>
              <Button
                variant="ghost"
                aria-label={`Move ${PANEL_LABELS[p.kind]} down`}
                disabled={i === panels.length - 1}
                onClick={() => set(move(panels, p.id, 1))}
              >
                ↓
              </Button>
              <label className="flex items-center gap-1 text-xs text-slate-500">
                <input
                  type="checkbox"
                  checked={!!p.wide}
                  onChange={(e) => patch(p.id, { wide: e.target.checked })}
                />
                Full width
              </label>
              <Button
                variant="ghost"
                aria-label={`Remove ${PANEL_LABELS[p.kind]}`}
                onClick={() => set(panels.filter((x) => x.id !== p.id))}
              >
                Remove
              </Button>
            </div>

            {p.kind === "weathermap" && (
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <Select
                  aria-label="Weathermap to show"
                  value={p.mapID ?? ""}
                  onChange={(e) => patch(p.id, { mapID: e.target.value })}
                >
                  <option value="">Choose a map…</option>
                  {(maps.data?.data ?? []).map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
                </Select>
                <span className="text-xs text-slate-500">
                  Only published maps render; a draft shows as unavailable.
                </span>
              </div>
            )}

            {p.kind === "metric" && (
              <div className="mt-2 flex flex-col gap-2">
                <div className="flex flex-wrap items-center gap-2">
                  <Select
                    aria-label="Metric"
                    value={p.metric ?? ""}
                    onChange={(e) => patch(p.id, { metric: e.target.value })}
                  >
                    <option value="">Choose a metric…</option>
                    {(metrics.data?.data ?? []).map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                  </Select>
                  <label className="flex items-center gap-1 text-xs text-slate-500">
                    <input
                      type="checkbox"
                      checked={!!p.rate}
                      onChange={(e) => patch(p.id, { rate: e.target.checked })}
                    />
                    Per-second rate
                  </label>
                </div>
                <div className="flex flex-wrap gap-2">
                  <Input
                    aria-label="Filter"
                    placeholder='Filter, e.g. site="FN"'
                    value={p.filter ?? ""}
                    onChange={(e) => patch(p.id, { filter: e.target.value })}
                  />
                  <Input
                    aria-label="Group by"
                    placeholder="Group by, e.g. site"
                    value={p.groupBy ?? ""}
                    onChange={(e) => patch(p.id, { groupBy: e.target.value })}
                  />
                  <Input
                    aria-label="Panel title"
                    placeholder="Title (optional)"
                    value={p.title ?? ""}
                    onChange={(e) => patch(p.id, { title: e.target.value })}
                  />
                </div>
                <p className="text-xs text-slate-500">
                  Counters such as <code>netinv_if_in_octets_total</code> only
                  make sense as a rate; gauges such as{" "}
                  <code>netinv_device_cpu_percent</code> do not. Tick the box
                  for the former.
                </p>
              </div>
            )}
          </div>
        ))}

        <div className="mt-1 flex flex-wrap items-center gap-2">
          <Select
            aria-label="Panel to add"
            value={adding}
            onChange={(e) => setAdding(e.target.value as PanelKind)}
          >
            {available.map((k) => (
              <option key={k} value={k}>
                {PANEL_LABELS[k]}
              </option>
            ))}
          </Select>
          <Button
            disabled={!available.includes(adding)}
            onClick={() =>
              set([
                ...panels,
                {
                  id: newPanelID(adding),
                  kind: adding,
                  wide: adding === "weathermap",
                },
              ])
            }
          >
            Add panel
          </Button>
          <div className="flex-1" />
          <Button variant="ghost" onClick={onClose}>
            {saving ? "Saving…" : "Done"}
          </Button>
        </div>

        <p className={cx("text-xs text-slate-500")}>
          Saved to your account, so the layout follows you to another browser.
          Removing every panel is allowed — “Reset to default” brings them back.
        </p>
      </div>
    </Card>
  );
}
