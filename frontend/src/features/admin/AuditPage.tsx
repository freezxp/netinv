// Audit viewer (doc 30 §9, Admin + Auditor): read-only by design.
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "../../api/client";
import { Card, EmptyState, Input } from "../../components/ui";

interface AuditRow {
  id: number;
  at: string;
  actor_kind: string;
  actor_id: string;
  action: string;
  resource_kind: string;
  resource_id: string;
  source_ip: string;
  trace_id: string;
}

export function AuditPage() {
  const [prefix, setPrefix] = useState("");
  const events = useQuery({
    queryKey: ["audit", prefix],
    queryFn: () =>
      api<{ data: AuditRow[] }>(
        `/audit-events?limit=200${prefix ? `&action_prefix=${encodeURIComponent(prefix)}` : ""}`,
      ),
  });
  return (
    <div className="mx-auto max-w-5xl">
      <div className="mb-3 flex items-center justify-between">
        <h1 className="text-xl font-semibold">Audit log</h1>
        <Input
          placeholder="action prefix, e.g. auth. or device."
          className="w-64"
          onKeyDown={(e) => {
            if (e.key === "Enter") setPrefix(e.currentTarget.value);
          }}
          onBlur={(e) => setPrefix(e.target.value)}
        />
      </div>
      <Card className="overflow-x-auto p-0">
        <table className="w-full min-w-[42rem] text-sm">
          <thead>
            <tr className="border-b border-slate-200 text-left text-xs uppercase text-slate-500 dark:border-slate-800">
              {["When", "Actor", "Action", "Resource", "Source IP", "Trace"].map(
                (h) => (
                  <th key={h} className="px-4 py-2 font-medium">
                    {h}
                  </th>
                ),
              )}
            </tr>
          </thead>
          <tbody>
            {events.data?.data.map((e) => (
              <tr
                key={e.id}
                className="border-b border-slate-100 dark:border-slate-800/60"
              >
                <td className="whitespace-nowrap px-4 py-1.5 text-slate-500">
                  {new Date(e.at).toLocaleString()}
                </td>
                <td className="px-4 py-1.5">
                  {e.actor_kind === "system" ? (
                    <span className="text-slate-500">system</span>
                  ) : (
                    <span className="mono text-xs">{e.actor_id.slice(0, 14)}</span>
                  )}
                </td>
                <td className="px-4 py-1.5 font-medium">{e.action}</td>
                <td className="px-4 py-1.5 text-slate-500">
                  {e.resource_kind && `${e.resource_kind} `}
                  <span className="mono text-xs">
                    {e.resource_id.slice(0, 14)}
                  </span>
                </td>
                <td className="mono px-4 py-1.5 text-xs">{e.source_ip}</td>
                <td className="mono px-4 py-1.5 text-xs text-slate-500">
                  {e.trace_id}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {events.data?.data.length === 0 && (
          <EmptyState>No events match.</EmptyState>
        )}
      </Card>
    </div>
  );
}
