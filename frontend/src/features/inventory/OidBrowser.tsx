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

  const asText = (vs: OIDValue[]) =>
    vs.map((v) => `${v.oid} = ${v.type}: ${v.value}`).join("\n");

  const copyAll = () => {
    void navigator.clipboard?.writeText(asText(walk.data?.data ?? []));
  };

  // The export re-walks rather than saving what is on screen. The browser asks
  // for 2000 objects because that is a sensible amount to scroll; a file that
  // silently stopped at the same place would be the worse kind of wrong, since
  // its whole purpose is to be read somewhere else — attached to an issue, or
  // sent to someone who cannot re-run the walk. If even the larger limit is
  // reached the file says so in its header rather than ending mid-tree without
  // comment.
  const EXPORT_LIMIT = 20000;
  const [exporting, setExporting] = useState(false);
  const [exportNote, setExportNote] = useState("");

  const exportTxt = async () => {
    setExporting(true);
    setExportNote("");
    try {
      const full = await api<{ data: OIDValue[]; truncated: boolean }>(
        `/devices/${device.id}/oids?root=${encodeURIComponent(root)}&limit=${EXPORT_LIMIT}`,
      );
      const at = new Date();
      const header = [
        "# NetInv — SNMP object dump",
        `# device:    ${device.sys_name || device.name}`,
        `# address:   ${device.mgmt_ip}`,
        `# root OID:  ${root}`,
        `# walked at: ${at.toISOString()}`,
        `# objects:   ${full.data.length}`,
        ...(full.truncated
          ? [
              `# WARNING:   truncated at ${EXPORT_LIMIT} objects — this is NOT the`,
              "#            whole subtree. Walk a narrower root to capture the rest.",
            ]
          : []),
        "",
      ].join("\n");
      const blob = new Blob([header + asText(full.data) + "\n"], {
        type: "text/plain;charset=utf-8",
      });
      const name = (device.sys_name || device.name || "device")
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, "");
      const stamp = at.toISOString().slice(0, 19).replace(/[:T]/g, "-");
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `snmp-${name}-${stamp}.txt`;
      a.click();
      URL.revokeObjectURL(url);
      setExportNote(
        full.truncated
          ? `exported ${full.data.length} — truncated`
          : `exported ${full.data.length}`,
      );
    } catch (e) {
      setExportNote(`export failed: ${(e as Error).message}`);
    } finally {
      setExporting(false);
    }
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
            {exportNote && ` · ${exportNote}`}
          </span>
          <Button variant="ghost" onClick={copyAll} disabled={!walk.data?.data.length}>
            Copy all
          </Button>
          <Button
            variant="ghost"
            onClick={() => void exportTxt()}
            disabled={exporting || !walk.data?.data.length}
            title="Re-walk this subtree and download every object as a .txt file"
          >
            {exporting ? "Exporting…" : "Export .txt"}
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
          {/* Fixed layout: without it a long OID column starves the value
              column, which then wraps to a dozen lines per row. */}
          <table className="w-full table-fixed text-xs">
            <colgroup>
              <col className="w-[46%]" />
              <col className="w-[12%]" />
              <col className="w-[42%]" />
            </colgroup>
            <tbody>
              {rows.slice(0, 1000).map((v) => (
                <tr
                  key={v.oid}
                  className="border-b border-slate-100 align-top dark:border-slate-800/60"
                >
                  <td className="mono truncate px-3 py-1 text-slate-500" title={v.oid}>
                    {v.oid}
                  </td>
                  <td className="px-3 py-1 text-slate-400">{v.type}</td>
                  <td className="mono truncate px-3 py-1" title={v.value}>
                    {v.value}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length > 1000 && (
            <div className="px-3 py-2 text-xs text-slate-500">
              Showing the first 1000 of {rows.length} — narrow the root OID or
              use the filter to see the rest.
            </div>
          )}
        </div>
        <div className="mt-2 text-xs text-slate-500">
          Walks run live against the device from the NetInv server. Large
          subtrees are capped at 2000 objects.
        </div>
      </Card>
    </div>
  );
}
