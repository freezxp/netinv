// Inventory list (doc 30 §4): filter bar synced to URL state (FR-DEV-04),
// keyset pagination. Virtualization arrives with the large-fleet sprint.
import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  useDeviceHealth,
  useDevices,
  useDuplicateDevices,
  useMergeDevices,
  useMoveDevicesToSite,
  useSites,
  type DuplicateGroup,
} from "../../api/hooks";
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
  const move = useMoveDevicesToSite();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [moveTo, setMoveTo] = useState("");
  const rows = devices.data?.data ?? [];
  // Selection is per page and cleared whenever the page changes: a checkbox
  // that survives a filter change would let someone move devices they can no
  // longer see.
  const clearSelection = () => {
    setSelected(new Set());
    move.reset();
  };
  const toggle = (id: string) =>
    setSelected((s) => {
      const next = new Set(s);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  const allShown = rows.length > 0 && rows.every((d) => selected.has(d.id));
  const health = useDeviceHealth();
  const siteName = (id: string) =>
    sites.data?.data.find((s) => s.id === id)?.name ?? id;

  const update = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    next.delete("cursor"); // filters reset pagination
    setCursors([]);
    clearSelection();
    setParams(next, { replace: true });
  };

  const goNext = () => {
    const next = devices.data?.next_cursor;
    if (!next) return;
    clearSelection();
    setCursors((c) => [...c, filters.cursor]);
    const p = new URLSearchParams(params);
    p.set("cursor", next);
    setParams(p, { replace: true });
  };

  const goPrev = () => {
    clearSelection();
    const prev = cursors[cursors.length - 1];
    setCursors((c) => c.slice(0, -1));
    const p = new URLSearchParams(params);
    if (prev) p.set("cursor", prev);
    else p.delete("cursor");
    setParams(p, { replace: true });
  };

  return (
    <div className="mx-auto max-w-6xl">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
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
          {["active", "pending", "unreachable", "disabled", "retired"].map(
            (s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ),
          )}
        </Select>
      </div>

      {selected.size > 0 && (
        <Card className="mb-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium">
              {selected.size} selected
            </span>
            <span className="text-sm text-slate-500">Move to site:</span>
            <Select
              className="w-48"
              value={moveTo}
              onChange={(e) => setMoveTo(e.target.value)}
            >
              <option value="">Choose a site…</option>
              {sites.data?.data.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>
            <Button
              disabled={!moveTo || move.isPending}
              onClick={() =>
                move.mutate(
                  { deviceIDs: [...selected], siteID: moveTo },
                  {
                    onSuccess: (r) => {
                      // Selection survives a partial failure so the ones that
                      // did not move are still in hand.
                      if (r.failed.length === 0) setSelected(new Set());
                    },
                  },
                )
              }
            >
              {move.isPending ? "Moving…" : "Move"}
            </Button>
            <Button variant="ghost" onClick={clearSelection}>
              Clear
            </Button>
          </div>
          {/* A site is a grouping, so moving a device between sites changes
              nothing it collects — but it does change which poller queue its
              jobs go to, and that is worth knowing before someone moves a
              device to a site nothing serves. */}
          <p className="mt-2 text-xs text-slate-500">
            A site is a grouping. Moving a device keeps its history, metrics and
            configuration — but its polling jobs move to the new site&apos;s
            queue, so the new site needs a poller that can reach it.
          </p>
          {move.isSuccess && (
            <div className="mt-2 text-sm">
              <span className="text-green-500">
                Moved {move.data.moved}{" "}
                {move.data.moved === 1 ? "device" : "devices"}.
              </span>
              {move.data.failed.length > 0 && (
                <span className="ml-2 text-red-500">
                  {move.data.failed.length} failed:{" "}
                  {move.data.failed[0].message}
                </span>
              )}
            </div>
          )}
          {move.isError && (
            <div className="mt-2 text-sm text-red-500">
              {(move.error as Error).message}
            </div>
          )}
        </Card>
      )}

      <DuplicatesCard />

      <Card className="overflow-x-auto p-0">
        <table className="w-full min-w-[52rem] text-sm">
          <thead>
            <tr className="border-b border-slate-200 text-left text-xs uppercase text-slate-500 dark:border-slate-800">
              {[
                "",
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
              ].map((h, i) =>
                i === 0 ? (
                  <th key="sel" className="w-8 px-4 py-2.5">
                    <input
                      type="checkbox"
                      aria-label="Select every device on this page"
                      checked={allShown}
                      onChange={() =>
                        setSelected(
                          allShown ? new Set() : new Set(rows.map((d) => d.id)),
                        )
                      }
                    />
                  </th>
                ) : (
                  <th key={h + i} className="px-4 py-2.5 font-medium">
                    {h}
                  </th>
                ),
              )}
            </tr>
          </thead>
          <tbody>
            {devices.data?.data.map((d) => (
              <tr
                key={d.id}
                className="border-b border-slate-100 hover:bg-slate-50 dark:border-slate-800/60 dark:hover:bg-slate-800/40"
              >
                <td className="px-4 py-2">
                  <input
                    type="checkbox"
                    aria-label={`Select ${d.name}`}
                    checked={selected.has(d.id)}
                    onChange={() => toggle(d.id)}
                  />
                </td>
                <td className="px-4 py-2">
                  <StatusBadge status={d.status} />
                </td>
                <td className="px-4 py-2 font-medium">
                  <Link to={`/devices/${d.id}`} className="hover:text-sky-500">
                    {d.sys_name || d.name}
                  </Link>
                  {/* The device's own sysName leads; the operator's label is
                      kept visible when it differs so neither is lost. */}
                  {d.sys_name && d.sys_name !== d.name && (
                    <span className="ml-2 text-xs font-normal text-slate-500">
                      {d.name}
                    </span>
                  )}
                </td>
                <td className="mono px-4 py-2">{d.mgmt_ip}</td>
                <td className="px-4 py-2">{siteName(d.site_id)}</td>
                <td className="px-4 py-2">{d.model || "—"}</td>
                <td className="px-4 py-2">
                  <StatCell
                    value={health.data?.data[d.id]?.cpu}
                    unit="%"
                    warn={70}
                    crit={85}
                  />
                </td>
                <td className="px-4 py-2">
                  <StatCell
                    value={health.data?.data[d.id]?.memory}
                    unit="%"
                    warn={80}
                    crit={90}
                  />
                </td>
                <td className="px-4 py-2">
                  <StatCell
                    value={health.data?.data[d.id]?.temp}
                    unit="°C"
                    warn={70}
                    crit={85}
                  />
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
        <Button variant="ghost" onClick={goPrev} disabled={!filters.cursor}>
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

/**
 * Devices that look like one device reachable on two management addresses.
 *
 * The card leads with *why* — the evidence and its strength — because the
 * decision it invites is not reversible, and "these two look alike" is not a
 * reason anyone should act on. It appears only when there is something to
 * report: a permanent empty panel teaches people to stop reading it.
 */
function DuplicatesCard() {
  const dups = useDuplicateDevices();
  const merge = useMergeDevices();
  const [confirm, setConfirm] = useState<{
    group: DuplicateGroup;
    keepID: string;
  } | null>(null);
  const groups = dups.data?.data ?? [];
  if (groups.length === 0) return null;

  return (
    <Card
      title={`Suspected duplicates (${groups.length})`}
      className="mb-3 border-l-4 border-amber-500"
    >
      <p className="text-xs text-slate-500">
        These records report the same identity, which usually means one device
        answering on two management addresses — discovered twice, polled twice,
        with its history split between them.
      </p>
      <div className="mt-3 flex flex-col gap-3">
        {groups.map((g) => (
          <div
            key={g.match + g.value}
            className="rounded border border-slate-200 p-2 dark:border-slate-800"
          >
            <div className="text-sm">
              {g.match === "serial" ? (
                <>
                  Same serial number <span className="mono">{g.value}</span> —{" "}
                  <span className="text-slate-500">
                    two chassis do not share a serial, so this is as close to
                    certain as inventory gets.
                  </span>
                </>
              ) : (
                <>
                  Same sysName <span className="mono">{g.value}</span> —{" "}
                  <span className="text-slate-500">
                    usually the hostname and usually unique, but two boxes can
                    be misconfigured with the same one. Check before merging.
                  </span>
                </>
              )}
            </div>
            <table className="mt-2 w-full text-sm">
              <tbody>
                {g.devices.map((d) => (
                  <tr key={d.id}>
                    <td className="py-1">
                      <Link
                        to={`/devices/${d.id}`}
                        className="hover:text-sky-500"
                      >
                        {d.name}
                      </Link>
                    </td>
                    <td className="mono py-1 text-xs text-slate-500">
                      {d.mgmt_ip}
                    </td>
                    <td className="py-1 text-xs text-slate-500">{d.status}</td>
                    <td className="py-1 text-right">
                      <Button
                        variant="ghost"
                        onClick={() => setConfirm({ group: g, keepID: d.id })}
                      >
                        Keep this one
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
      </div>

      {confirm && (
        <MergeConfirm
          group={confirm.group}
          keepID={confirm.keepID}
          pending={merge.isPending}
          error={merge.error as Error | null}
          result={merge.data ?? null}
          onCancel={() => {
            merge.reset();
            setConfirm(null);
          }}
          onConfirm={() => {
            const dup = confirm.group.devices.find(
              (d) => d.id !== confirm.keepID,
            );
            if (dup) merge.mutate({ keepID: confirm.keepID, dupID: dup.id });
          }}
        />
      )}
    </Card>
  );
}

function MergeConfirm({
  group,
  keepID,
  pending,
  error,
  result,
  onCancel,
  onConfirm,
}: {
  group: DuplicateGroup;
  keepID: string;
  pending: boolean;
  error: Error | null;
  result: {
    kept_name: string;
    retired_name: string;
    alt_addresses: string[];
  } | null;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const keep = group.devices.find((d) => d.id === keepID);
  const dup = group.devices.find((d) => d.id !== keepID);
  if (!keep || !dup) return null;
  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50 p-4">
      <Card title="Merge duplicate" className="w-full max-w-[30rem]">
        {result ? (
          <>
            <p className="text-sm">
              <span className="font-medium">{result.retired_name}</span> was
              retired into{" "}
              <span className="font-medium">{result.kept_name}</span>.
            </p>
            <p className="mt-2 text-xs text-slate-500">
              Addresses now recorded on it:{" "}
              <span className="mono">{result.alt_addresses.join(", ")}</span>
            </p>
            <div className="mt-3 flex justify-end">
              <Button onClick={onCancel}>Close</Button>
            </div>
          </>
        ) : (
          <>
            <p className="text-sm">
              Keep <span className="font-medium">{keep.name}</span> (
              <span className="mono text-xs">{keep.mgmt_ip}</span>) and retire{" "}
              <span className="font-medium">{dup.name}</span> (
              <span className="mono text-xs">{dup.mgmt_ip}</span>).
            </p>
            <p className="mt-2 text-xs text-slate-500">
              The retired device&apos;s addresses and tags move to the one you
              keep, and its polling stops. It is retired, not deleted.
            </p>
            <p className="mt-2 text-xs text-slate-500">
              <strong>Metrics and history are not moved.</strong> Every series
              is keyed to the device that collected it, so relabelling a year of
              data would be inventing a continuity that never existed. What was
              collected under {dup.name} stays readable under it; everything
              from now on is collected under {keep.name}.
            </p>
            {error && (
              <div className="mt-2 text-sm text-red-500">{error.message}</div>
            )}
            <div className="mt-3 flex justify-end gap-2">
              <Button variant="ghost" onClick={onCancel}>
                Cancel
              </Button>
              <Button variant="danger" disabled={pending} onClick={onConfirm}>
                {pending ? "Merging…" : "Merge"}
              </Button>
            </div>
          </>
        )}
      </Card>
    </div>
  );
}
