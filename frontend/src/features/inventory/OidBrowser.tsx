// OID browser (doc 30 §5): dump what a device actually exposes over SNMP.
// This is the tool for working out which MIBs a new platform supports before
// writing a connector for it — and for answering "why is this metric empty?".
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "../../api/client";
import type { Device } from "../../api/types";
import { Button, Card, EmptyState, Input, Select } from "../../components/ui";

interface OIDValue {
  oid: string;
  type: string;
  value: string;
}

// Starting points that answer most questions, without making the user know
// OIDs by heart.
const roots = [
  { label: "Standard (mib-2)", oid: ".1.3.6.1.2.1" },
  { label: "System group", oid: ".1.3.6.1.2.1.1" },
  { label: "Interfaces", oid: ".1.3.6.1.2.1.2.2.1" },
  { label: "Interfaces (extended)", oid: ".1.3.6.1.2.1.31.1.1.1" },
  { label: "Host resources", oid: ".1.3.6.1.2.1.25" },
  { label: "UCD-SNMP (Linux CPU/mem)", oid: ".1.3.6.1.4.1.2021" },
  { label: "LLDP neighbours", oid: ".1.0.8802.1.1.2.1.4" },
  { label: "Vendor tree (all private MIBs)", oid: ".1.3.6.1.4.1" },
];

export function OidBrowser({
  device,
  onClose,
}: {
  device: Device;
  onClose: () => void;
}) {
  const [root, setRoot] = useState(roots[0].oid);
  const [pending, setPending] = useState(roots[0].oid);
  const [filter, setFilter] = useState("");

  const walk = useQuery({
    queryKey: ["oids", device.id, root],
    queryFn: () =>
      api<{ data: OIDValue[]; truncated: boolean }>(
        `/devices/${device.id}/oids?root=${encodeURIComponent(root)}&limit=2000`,
      ),
    retry: false,
    staleTime: 30_000,
  });

  const rows = (walk.data?.data ?? []).filter((v) => {
    if (!filter) return true;
    const f = filter.toLowerCase();
    return (
      v.oid.includes(filter) ||
      v.value.toLowerCase().includes(f) ||
      v.type.toLowerCase().includes(f)
    );
  });

  const copyAll = () => {
    const text = (walk.data?.data ?? [])
      .map((v) => `${v.oid} = ${v.type}: ${v.value}`)
      .join("\n");
    void navigator.clipboard?.writeText(text);
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50 p-6">
      <Card
        title={`SNMP objects — ${device.sys_name || device.name} (${device.mgmt_ip})`}
        className="flex h-full w-full max-w-4xl flex-col"
      >
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <Select
            value={roots.some((r) => r.oid === pending) ? pending : ""}
            onChange={(e) => {
              setPending(e.target.value);
              setRoot(e.target.value);
            }}
          >
            {roots.map((r) => (
              <option key={r.oid} value={r.oid}>
                {r.label}
              </option>
            ))}
            <option value="">Custom…</option>
          </Select>
          <Input
            className="w-56"
            value={pending}
            placeholder="root OID, e.g. .1.3.6.1.4.1.25053"
            onChange={(e) => setPending(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") setRoot(pending);
            }}
          />
          <Button onClick={() => setRoot(pending)} disabled={walk.isFetching}>
            {walk.isFetching ? "Walking…" : "Walk"}
          </Button>
          <Input
            className="w-48"
            placeholder="filter results…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
          <div className="flex-1" />
          <span className="text-xs text-slate-500">
            {walk.data ? `${rows.length} of ${walk.data.data.length}` : ""}
            {walk.data?.truncated && " (truncated)"}
          </span>
          <Button variant="ghost" onClick={copyAll} disabled={!walk.data?.data.length}>
            Copy all
          </Button>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-auto rounded border border-slate-200 dark:border-slate-800">
          {walk.isError && (
            <EmptyState>
              Walk failed: {(walk.error as Error).message}. The device may be
              unreachable from the NetInv server, or the credential may not
              permit this subtree.
            </EmptyState>
          )}
          {walk.isFetching && !walk.data && (
            <EmptyState>Walking the device…</EmptyState>
          )}
          {walk.data && rows.length === 0 && (
            <EmptyState>
              Nothing returned under this OID — the device doesn't implement it.
            </EmptyState>
          )}
          <table className="w-full text-xs">
            <tbody>
              {rows.map((v) => (
                <tr
                  key={v.oid}
                  className="border-b border-slate-100 align-top dark:border-slate-800/60"
                >
                  <td className="mono px-3 py-1 whitespace-nowrap text-slate-500">
                    {v.oid}
                  </td>
                  <td className="px-3 py-1 whitespace-nowrap text-slate-400">
                    {v.type}
                  </td>
                  <td className="mono px-3 py-1 break-all">{v.value}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="mt-2 text-xs text-slate-500">
          Walks run live against the device from the NetInv server. Large
          subtrees are capped at 2000 objects.
        </div>
      </Card>
    </div>
  );
}
