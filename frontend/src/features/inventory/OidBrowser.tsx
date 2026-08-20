// OID browser (doc 30 §5): dump what a device actually exposes over SNMP.
// This is the tool for working out which MIBs a new platform supports before
// writing a connector for it — and for answering "why is this metric empty?".
import { useRef, useState } from "react";
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

  // The export re-walks the *whole tree*, not the subtree being browsed and not
  // what is on screen. Its purpose is to hand somebody everything a device
  // exposes — for connector work, or for a question nobody has asked yet — so a
  // file that stopped early would be the worse kind of wrong: it is read
  // somewhere else, by someone who cannot re-run the walk and cannot tell a
  // device that ends at object 1000 from a file that does.
  //
  // Four roots, not one. `.1` cannot be walked at all: BER packs the first two
  // arcs of an OID into a single byte, so a one-arc OID has no encoding and
  // gosnmp rejects it with "unable to marshal OID". Every child of iso is
  // walked instead, which is both encodable and exhaustive. `.1.0` is the one
  // that matters in practice — LLDP lives at .1.0.8802.1.1.2, outside the
  // internet subtree, so walking only `.1.3` silently drops neighbour data,
  // which is exactly what a dump gets wanted for. `.1.1` and `.1.2` are almost
  // always empty and cost one round trip each to prove it.
  //
  // Sequential, not parallel: four concurrent walks against one agent is a
  // sure way to make a device drop responses and produce a short file that
  // looks complete.
  //
  // This is slow by nature: tens of thousands of objects at a 5s SNMP timeout,
  // minutes rather than seconds on a large device.
  const EXPORT_ROOTS = [".1.0", ".1.1", ".1.2", ".1.3"];
  const EXPORT_LIMIT = 500000;
  // Below nginx's proxy_read_timeout so the request aborts with something we
  // can explain, rather than becoming a 504 the user has to interpret.
  const EXPORT_TIMEOUT_MS = 280_000;

  const [exporting, setExporting] = useState(false);
  const [exportNote, setExportNote] = useState("");
  const abortRef = useRef<AbortController | null>(null);

  const save = (text: string, suffix: string) => {
    const name = (device.sys_name || device.name || "device")
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "");
    const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-");
    const url = URL.createObjectURL(
      new Blob([text], { type: "text/plain;charset=utf-8" }),
    );
    const a = document.createElement("a");
    a.href = url;
    a.download = `snmp-${name}-${suffix}${stamp}.txt`;
    a.click();
    URL.revokeObjectURL(url);
  };

  // runExport walks a list of roots and downloads the result.
  //
  // Everything here exists because of one real export: 407271 objects, 33MB,
  // dominated by an edge router's routing table (165k ipCidrRoute rows). A walk
  // that size takes minutes, and with no count, no elapsed time and no way out
  // it is indistinguishable from a hang — which is exactly how it was reported.
  //
  // So: progress after every root, a cancel button, a timeout below nginx's,
  // and — the part that matters — whatever was collected is still offered as a
  // file, clearly marked partial. A cancelled walk of a big router is usually
  // still the most useful thing anyone has.
  const runExport = async (roots: string[], what: string) => {
    const ctl = new AbortController();
    abortRef.current = ctl;
    const timer = window.setTimeout(() => ctl.abort("timeout"), EXPORT_TIMEOUT_MS);
    const started = Date.now();
    setExporting(true);
    setExportNote("starting…");

    const collected: OIDValue[] = [];
    const perRoot: string[] = [];
    let anyTruncated = false;
    let stopped = "";

    try {
      for (const r of roots) {
        setExportNote(
          `walking ${r} — ${collected.length} objects, ${Math.round((Date.now() - started) / 1000)}s`,
        );
        const part = await api<{ data: OIDValue[]; truncated: boolean }>(
          `/devices/${device.id}/oids?root=${encodeURIComponent(r)}&limit=${EXPORT_LIMIT}`,
          { signal: ctl.signal },
        );
        collected.push(...part.data);
        anyTruncated = anyTruncated || part.truncated;
        perRoot.push(
          `#   ${r.padEnd(6)} ${part.data.length}${part.truncated ? "  (CEILING HIT)" : ""}`,
        );
      }
    } catch (e) {
      stopped =
        ctl.signal.reason === "timeout"
          ? `timed out after ${Math.round(EXPORT_TIMEOUT_MS / 1000)}s`
          : ctl.signal.aborted
            ? "cancelled"
            : (e as Error).message;
    } finally {
      clearTimeout(timer);
      abortRef.current = null;
    }

    if (!collected.length) {
      setExporting(false);
      setExportNote(stopped ? `export ${stopped}` : "nothing returned");
      return;
    }

    const complete = !stopped && !anyTruncated;
    const header = [
      "# NetInv — SNMP object dump",
      `# device:    ${device.sys_name || device.name}`,
      `# address:   ${device.mgmt_ip}`,
      `# scope:     ${what}`,
      ...perRoot,
      `# walked at: ${new Date().toISOString()}`,
      `# objects:   ${collected.length}`,
      ...(complete
        ? ["# complete:  yes — every root walked to the end"]
        : [
            "# PARTIAL:   this dump is INCOMPLETE. Do not read an absent OID",
            "#            here as evidence the device does not implement it.",
            ...(stopped ? [`#            reason: ${stopped}`] : []),
            ...(anyTruncated
              ? [`#            reason: hit the ${EXPORT_LIMIT}-object ceiling`]
              : []),
            `#            roots walked: ${perRoot.length} of ${roots.length}`,
          ]),
      "",
    ].join("\n");

    save(header + asText(collected) + "\n", complete ? "" : "partial-");
    setExporting(false);
    setExportNote(
      complete
        ? `exported ${collected.length} objects`
        : `exported ${collected.length} — PARTIAL (${stopped || "ceiling hit"})`,
    );
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
          {/* Subtree first: on a router, "everything" means the routing table
              too — one real export ran to 407k objects and 33MB, most of it
              ipCidrRoute. Someone chasing a CPU OID wants the branch they are
              looking at, not the RIB. */}
          <Button
            variant="ghost"
            onClick={() => void runExport([root], `subtree ${root}`)}
            disabled={exporting}
            title={`Walk ${root} only and download it as a .txt file`}
          >
            Export subtree
          </Button>
          <Button
            variant="ghost"
            onClick={() =>
              void runExport(
                EXPORT_ROOTS,
                "whole tree — every child of iso (.1 itself has no BER encoding)",
              )
            }
            disabled={exporting}
            title="Walk the device's whole OID tree. Minutes on a large device; you can cancel and still keep what was collected."
          >
            Export all (.txt)
          </Button>
          {exporting && (
            <Button
              variant="ghost"
              onClick={() => abortRef.current?.abort("cancelled")}
              title="Stop walking and save what has been collected so far"
            >
              Cancel
            </Button>
          )}
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
