// Device removal (FR-DEV-08): retire is the soft, reversible step that keeps
// history; permanent deletion is Admin-only, requires the device to be retired
// first, and takes a typed confirmation (doc 30 §0 destructive-action rule).
import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import type { Device } from "../../api/types";
import { hasPermissionRole, useAuthStore } from "../auth/store";
import { Button, Card, Input } from "../../components/ui";
import { OidBrowser } from "./OidBrowser";
import { DeviceFormModal } from "./DeviceForm";

export function DeviceActions({ device }: { device: Device }) {
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);
  const isAdmin = hasPermissionRole(user);
  const [confirming, setConfirming] = useState(false);
  const [browsing, setBrowsing] = useState(false);
  const [editing, setEditing] = useState(false);

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["devices"] });
    qc.invalidateQueries({ queryKey: ["device", device.id] });
  };
  const retire = useMutation({
    mutationFn: () => api(`/devices/${device.id}/retire`, { method: "POST" }),
    onSuccess: invalidate,
  });
  const restore = useMutation({
    mutationFn: () => api(`/devices/${device.id}/enable`, { method: "POST" }),
    onSuccess: invalidate,
  });
  const purge = useMutation({
    mutationFn: () => api(`/devices/${device.id}`, { method: "DELETE" }),
    onSuccess: () => {
      setConfirming(false);
      invalidate();
    },
  });

  if (device.status === "retired") {
    return (
      <>
        <span className="mr-2 text-xs text-slate-500">retired</span>
        <Button variant="ghost" onClick={() => restore.mutate()}>
          Restore
        </Button>
        {isAdmin && (
          <Button variant="danger" onClick={() => setConfirming(true)}>
            Delete
          </Button>
        )}
        {confirming && (
          <ConfirmPurge
            device={device}
            pending={purge.isPending}
            error={purge.error as Error | null}
            onCancel={() => setConfirming(false)}
            onConfirm={() => purge.mutate()}
          />
        )}
      </>
    );
  }

  return (
    <>
      <Button
        variant="ghost"
        onClick={() => setEditing(true)}
        title="Change name, site, credential, tags, or the site's uplink rate"
      >
        Edit
      </Button>
      <Button
        variant="ghost"
        onClick={() => setBrowsing(true)}
        title="Show every SNMP object this device exposes"
      >
        OIDs
      </Button>
      <Button
        variant="ghost"
        disabled={retire.isPending}
        onClick={() => retire.mutate()}
        title="Stop polling and hide from inventory. History is kept and it can be restored."
      >
        Retire
      </Button>
      {browsing && (
        <OidBrowser device={device} onClose={() => setBrowsing(false)} />
      )}
      {editing && (
        <DeviceFormModal existing={device} onClose={() => setEditing(false)} />
      )}
    </>
  );
}

function ConfirmPurge({
  device,
  pending,
  error,
  onCancel,
  onConfirm,
}: {
  device: Device;
  pending: boolean;
  error: Error | null;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const [typed, setTyped] = useState("");
  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50 p-4">
      <Card title="Delete device permanently" className="w-full max-w-[26rem]">
        <p className="text-sm">
          This removes <span className="font-medium">{device.name}</span> (
          <span className="mono">{device.mgmt_ip}</span>) and everything
          inventory holds about it — interfaces, polling schedules, topology
          links and change history. It cannot be undone.
        </p>
        <p className="mt-2 text-xs text-slate-500">
          Collected metrics are not deleted; they expire under the retention
          policy. Audit records of your actions are always kept.
        </p>
        <label className="mt-3 block text-xs text-slate-500">
          Type the device name to confirm:
          <Input
            className="mt-1 w-full"
            value={typed}
            autoFocus
            onChange={(e) => setTyped(e.target.value)}
            placeholder={device.name}
          />
        </label>
        {error && <div className="mt-2 text-sm text-red-500">{error.message}</div>}
        <div className="mt-3 flex justify-end gap-2">
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={typed !== device.name || pending}
            onClick={onConfirm}
          >
            {pending ? "Deleting…" : "Delete permanently"}
          </Button>
        </div>
      </Card>
    </div>
  );
}
