// Inventory list (doc 30 §4): filter bar synced to URL state (FR-DEV-04),
// keyset pagination. Virtualization arrives with the large-fleet sprint.
import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useDeviceHealth, useDevices, useSites } from "../../api/hooks";
import { useAuthStore } from "../auth/store";
import {
  Button,
  Card,
  EmptyState,
  Input,
  Select,
  StatusBadge,
} from "../../components/ui";
import { DeviceFormModal } from "./DeviceForm";
import { DeviceActions } from "./DeviceActions";

// Export uses a token-authorized fetch so the download carries auth.
async function download(format: "csv" | "xlsx", filter: string, q: string) {
  const params = new URLSearchParams({ format });
  if (filter) params.set("filter", filter);
  if (q) params.set("q", q);
  const token = useAuthStore.getState().accessToken;
  const res = await fetch(`/api/v1/exports/inventory?${params}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) return;
  const blob = await res.blob();
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = `netinv-inventory.${format}`;
  a.click();
  URL.revokeObjectURL(a.href);
}

export function InventoryPage() {
  const [params, setParams] = useSearchParams();
  const [cursors, setCursors] = useState<string[]>([]);
  const [showForm, setShowForm] = useState(false);
  const filters = {
    q: params.get("q") ?? "",
    site: params.get("site") ?? "",
    status: params.get("status") ?? "",
    cursor: params.get("cursor") ?? "",
  };
  const devices = useDevices(filters);
  const sites = useSites();
  const health = useDeviceHealth();
  const siteName = (id: string) =>
    sites.data?.data.find((s) => s.id === id)?.name ?? id;

  const update = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    next.delete("cursor"); // filters reset pagination
    setCursors([]);
    setParams(next, { replace: true });
  };

  const goNext = () => {
    const next = devices.data?.next_cursor;
    if (!next) return;
    setCursors((c) => [...c, filters.cursor]);
    const p = new URLSearchParams(params);
    p.set("cursor", next);
    setParams(p, { replace: true });
  };

  const goPrev = () => {
    const prev = cursors[cursors.length - 1];
    setCursors((c) => c.slice(0, -1));
    const p = new URLSearchParams(params);
    if (prev) p.set("cursor", prev);
    else p.delete("cursor");
    setParams(p, { replace: true });
  };

  return (
    <div className="mx-auto max-w-6xl">
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-xl font-semibold">Inventory</h1>
        <div className="flex items-center gap-2">
          <span className="text-sm text-slate-500">
            {devices.data ? `${devices.data.data.length} shown` : ""}
          </span>
          <Button
            variant="ghost"
            onClick={() =>
              download(
                "csv",
                [
                  filters.site && `site:eq:${filters.site}`,
                  filters.status && `status:eq:${filters.status}`,
                ]
                  .filter(Boolean)
                  .join(","),
                filters.q,
              )
            }
          >
            CSV
          </Button>
          <Button
            variant="ghost"
            onClick={() =>
              download(
                "xlsx",
                [
                  filters.site && `site:eq:${filters.site}`,
                  filters.status && `status:eq:${filters.status}`,
                ]
                  .filter(Boolean)
                  .join(","),
                filters.q,
              )
            }
          >
            Excel
          </Button>
          <Button onClick={() => setShowForm(true)}>Add device</Button>
        </div>
      </div>
      {showForm && <DeviceFormModal onClose={() => setShowForm(false)} />}

      <div className="mb-4 flex flex-wrap gap-2">
        <Input
          placeholder="Search name, IP, serial…"
          defaultValue={filters.q}
          className="w-64"
          onKeyDown={(e) => {
            if (e.key === "Enter") update("q", e.currentTarget.value);
          }}
          onBlur={(e) => update("q", e.target.value)}
        />
        <Select
          value={filters.site}
          onChange={(e) => update("site", e.target.value)}
        >
          <option value="">All sites</option>
          {sites.data?.data.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </Select>
        <Select
          value={filters.status}
          onChange={(e) => update("status", e.target.value)}
        >
          <option value="">All statuses</option>
          {["active", "pending", "unreachable", "disabled", "retired"].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </Select>
      </div>

      <Card className="overflow-x-auto p-0">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-slate-200 text-left text-xs uppercase text-slate-500 dark:border-slate-800">
              {[
                "Status",
                "Name",
                "Management IP",
                "Site",
                "Model",
                "CPU",
                "Memory",
                "Temp",
                "Tags",
                "",
              ].map((h, i) => (
                <th key={h + i} className="px-4 py-2.5 font-medium">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {devices.data?.data.map((d) => (
              <tr
                key={d.id}
                className="border-b border-slate-100 hover:bg-slate-50 dark:border-slate-800/60 dark:hover:bg-slate-800/40"
              >
                <td className="px-4 py-2">
                  <StatusBadge status={d.status} />
                </td>
                <td className="px-4 py-2 font-medium">
                  <Link to={`/devices/${d.id}`} className="hover:text-sky-500">
                    {d.name}
                  </Link>
                </td>
                <td className="mono px-4 py-2">{d.mgmt_ip}</td>
                <td className="px-4 py-2">{siteName(d.site_id)}</td>
                <td className="px-4 py-2">{d.model || "—"}</td>
                <td className="px-4 py-2">
                  <StatCell value={health.data?.data[d.id]?.cpu} unit="%" warn={70} crit={85} />
                </td>
                <td className="px-4 py-2">
                  <StatCell value={health.data?.data[d.id]?.memory} unit="%" warn={80} crit={90} />
                </td>
                <td className="px-4 py-2">
                  <StatCell value={health.data?.data[d.id]?.temp} unit="°C" warn={70} crit={85} />
                </td>
                <td className="px-4 py-2 text-slate-500">
                  {d.tags.join(", ") || "—"}
                </td>
                <td className="whitespace-nowrap px-4 py-2 text-right">
                  <DeviceActions device={d} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {devices.data?.data.length === 0 && (
          <EmptyState>
            No devices match. Adjust filters or add a device via the API.
          </EmptyState>
        )}
        {devices.isError && (
          <EmptyState>Failed to load devices — retrying…</EmptyState>
        )}
      </Card>

      <div className="mt-3 flex justify-end gap-2">
        <Button
          variant="ghost"
          onClick={goPrev}
          disabled={!filters.cursor}
        >
          ← Previous
        </Button>
        <Button
          variant="ghost"
          onClick={goNext}
          disabled={!devices.data?.next_cursor}
        >
          Next →
        </Button>
      </div>
    </div>
  );
}

// Compact live stat with threshold tinting. A device whose connector exposes
// no health simply shows a dash rather than a misleading zero.
function StatCell({
  value,
  unit,
  warn,
  crit,
}: {
  value?: number;
  unit: string;
  warn: number;
  crit: number;
}) {
  if (value === undefined) return <span className="text-slate-500">—</span>;
  const tone =
    value >= crit ? "text-red-500" : value >= warn ? "text-amber-500" : "";
  return (
    <span className={`mono ${tone}`}>
      {value.toFixed(unit === "°C" ? 1 : 0)}
      {unit}
    </span>
  );
}
