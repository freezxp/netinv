// SNMP traps (doc 36 §6, doc 30): what devices reported about themselves.
// The page lists the fleet; TrapsTable is reused on the device detail page.
// Alerting on traps happens through ordinary rules over netinv_snmp_trap, so
// this view is for reading what arrived, not for acting on it.
import { Fragment, useState } from "react";
import { Link } from "react-router-dom";
import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "../../api/client";
import { Button, Card, EmptyState, Input, cx } from "../../components/ui";

interface Varbind {
  oid: string;
  type: string;
  value: string;
}

interface TrapRow {
  id: number;
  received_at: string;
  source: string;
  agent_addr?: string;
  device_id?: string;
  device_name?: string;
  version: "1" | "2c";
  inform: boolean;
  trap_oid: string;
  trap_name?: string;
  if_index?: number;
  if_name?: string;
  varbinds: Varbind[];
}

interface Filter {
  deviceID?: string;
  unknownOnly?: boolean;
  q?: string;
}

function useTraps(f: Filter) {
  return useInfiniteQuery({
    queryKey: ["traps", f],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => {
      const p = new URLSearchParams({ limit: "100" });
      if (f.deviceID) p.set("device_id", f.deviceID);
      if (f.unknownOnly) p.set("unknown", "1");
      if (f.q) p.set("q", f.q);
      if (pageParam) p.set("before", String(pageParam));
      return api<{ data: TrapRow[]; next_before: number }>(`/events/traps?${p}`);
    },
    // A short page means the end; otherwise continue below the last id.
    getNextPageParam: (last) =>
      last.data.length === 100 ? last.next_before : undefined,
    refetchInterval: 30_000,
  });
}

export function TrapsPage() {
  const [q, setQ] = useState("");
  const [unknownOnly, setUnknownOnly] = useState(false);
  return (
    <div className="mx-auto max-w-6xl">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-semibold">SNMP traps</h1>
        <div className="flex flex-wrap items-center gap-3">
          <label className="flex items-center gap-2 text-sm text-slate-600 dark:text-slate-300">
            <input
              type="checkbox"
              checked={unknownOnly}
              onChange={(e) => setUnknownOnly(e.target.checked)}
            />
            From unknown sources only
          </label>
          <Input
            placeholder="trap name or OID prefix"
            className="w-56"
            onKeyDown={(e) => {
              if (e.key === "Enter") setQ(e.currentTarget.value.trim());
            }}
            onBlur={(e) => setQ(e.target.value.trim())}
          />
        </div>
      </div>
      <TrapsTable filter={{ q, unknownOnly }} showDevice />
    </div>
  );
}

export function TrapsTable({
  filter,
  showDevice = false,
}: {
  filter: Filter;
  showDevice?: boolean;
}) {
  const traps = useTraps(filter);
  const [open, setOpen] = useState<number | null>(null);
  const rows = traps.data?.pages.flatMap((p) => p.data) ?? [];
  const cols = ["When", ...(showDevice ? ["Device"] : []), "Trap", "Interface", ""];

  return (
    <Card className="overflow-x-auto p-0">
      <table className="w-full min-w-[40rem] text-sm">
        <thead>
          <tr className="border-b border-slate-200 text-left text-xs uppercase text-slate-500 dark:border-slate-800">
            {cols.map((h, i) => (
              <th key={i} className="px-4 py-2 font-medium">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((t) => (
            <Fragment key={t.id}>
              <tr
                className="cursor-pointer border-b border-slate-100 hover:bg-slate-50 dark:border-slate-800/60 dark:hover:bg-slate-900/40"
                onClick={() => setOpen(open === t.id ? null : t.id)}
              >
                <td className="whitespace-nowrap px-4 py-1.5 text-slate-500">
                  {new Date(t.received_at).toLocaleString()}
                </td>
                {showDevice && (
                  <td className="px-4 py-1.5">
                    {t.device_id ? (
                      <Link
                        to={`/devices/${t.device_id}`}
                        className="text-sky-600 hover:underline dark:text-sky-400"
                        onClick={(e) => e.stopPropagation()}
                      >
                        {t.device_name ?? t.device_id}
                      </Link>
                    ) : (
                      // Unknown sources are the ones worth noticing: a device
                      // that is sending traps but is not in inventory, or a
                      // gateway sending from an address nobody has claimed.
                      <span
                        className="text-amber-600 dark:text-amber-400"
                        title="No device claims this address"
                      >
                        {t.source}
                        {t.agent_addr && t.agent_addr !== t.source
                          ? ` (agent ${t.agent_addr})`
                          : ""}
                      </span>
                    )}
                  </td>
                )}
                <td className="px-4 py-1.5">
                  <span className={cx(!t.trap_name && "font-mono text-xs")}>
                    {t.trap_name ?? t.trap_oid}
                  </span>
                  <span className="ml-2 text-xs text-slate-400">
                    v{t.version}
                    {t.inform ? " inform" : ""}
                  </span>
                </td>
                <td className="px-4 py-1.5">
                  {t.if_name ?? (t.if_index != null ? `ifIndex ${t.if_index}` : "")}
                </td>
                <td className="px-4 py-1.5 text-right text-xs text-slate-400">
                  {t.varbinds.length} var{t.varbinds.length === 1 ? "" : "s"}
                </td>
              </tr>
              {open === t.id && (
                <tr className="border-b border-slate-100 bg-slate-50 dark:border-slate-800/60 dark:bg-slate-900/40">
                  <td colSpan={cols.length} className="px-4 py-2">
                    <div className="mb-1 font-mono text-xs text-slate-500">
                      {t.trap_oid}
                    </div>
                    {t.varbinds.length === 0 ? (
                      <div className="text-xs text-slate-500">No variables.</div>
                    ) : (
                      <table className="w-full text-xs">
                        <tbody>
                          {t.varbinds.map((v, i) => (
                            <tr key={i}>
                              <td className="py-0.5 pr-4 font-mono text-slate-500">
                                {v.oid}
                              </td>
                              <td className="py-0.5 pr-4 text-slate-400">{v.type}</td>
                              <td className="break-all py-0.5 font-mono">{v.value}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    )}
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </table>
      {traps.isSuccess && rows.length === 0 && (
        <EmptyState>
          No traps received
          {filter.deviceID ? " from this device" : ""}. Point the device's trap
          destination at this server on UDP 162.
        </EmptyState>
      )}
      {traps.hasNextPage && (
        <div className="p-3 text-center">
          <Button
            variant="ghost"
            disabled={traps.isFetchingNextPage}
            onClick={() => traps.fetchNextPage()}
          >
            {traps.isFetchingNextPage ? "Loading…" : "Older traps"}
          </Button>
        </div>
      )}
    </Card>
  );
}
