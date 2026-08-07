// Device onboarding/edit form (doc 30 §4). Credential picker needs admin
// visibility of /credentials; operators pick from an id they know or keep
// the current one on edit.
import { type FormEvent, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import type { Device, Paged } from "../../api/types";
import { useAuthStore, hasPermissionRole } from "../auth/store";
import { Button, Card, Input, Select } from "../../components/ui";
import { useSites } from "../../api/hooks";

interface Credential {
  id: string;
  name: string;
  kind: string;
}

function useCredentials(enabled: boolean) {
  return useQuery({
    queryKey: ["credentials"],
    queryFn: () => api<Paged<Credential>>("/credentials"),
    enabled,
    staleTime: 60_000,
  });
}

export interface DeviceInput {
  name: string;
  mgmt_ip: string;
  site_id: string;
  credential_id: string;
  snmp_port?: number;
  tags?: string[];
  notes?: string;
}

export function DeviceFormModal({
  existing,
  onClose,
}: {
  existing?: Device;
  onClose: () => void;
}) {
  const user = useAuthStore((s) => s.user);
  const isAdmin = hasPermissionRole(user); // admin only for credential listing
  const sites = useSites();
  const creds = useCredentials(isAdmin);
  const qc = useQueryClient();

  const [form, setForm] = useState<DeviceInput>({
    name: existing?.name ?? "",
    mgmt_ip: existing?.mgmt_ip ?? "",
    site_id: existing?.site_id ?? "",
    credential_id: existing?.credential_id ?? "",
    snmp_port: undefined,
    tags: existing?.tags ?? [],
  });
  const set = (k: keyof DeviceInput, v: unknown) =>
    setForm((f) => ({ ...f, [k]: v }));

  const save = useMutation({
    mutationFn: (input: DeviceInput) =>
      existing
        ? api<Device>(`/devices/${existing.id}`, {
            method: "PATCH",
            body: JSON.stringify(input),
          })
        : api<Device>("/devices", {
            method: "POST",
            body: JSON.stringify(input),
          }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["devices"] });
      onClose();
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(form);
  };

  return (
    <div className="fixed inset-0 z-20 flex items-center justify-center bg-black/40">
      <Card
        title={existing ? `Edit ${existing.name}` : "Add device"}
        className="w-96"
      >
        <form onSubmit={submit} className="flex flex-col gap-3">
          <Input
            placeholder="Display name"
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
            required
          />
          <Input
            placeholder="Management IP"
            value={form.mgmt_ip}
            onChange={(e) => set("mgmt_ip", e.target.value)}
            disabled={!!existing}
            required
          />
          <Select
            value={form.site_id}
            onChange={(e) => set("site_id", e.target.value)}
            required
          >
            <option value="">Select site…</option>
            {sites.data?.data.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </Select>
          {isAdmin ? (
            <Select
              value={form.credential_id}
              onChange={(e) => set("credential_id", e.target.value)}
              required={!existing}
            >
              <option value="">
                {existing ? "Keep current credential" : "Select credential…"}
              </option>
              {creds.data?.data.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name} ({c.kind})
                </option>
              ))}
            </Select>
          ) : (
            <Input
              placeholder="Credential ID"
              value={form.credential_id}
              onChange={(e) => set("credential_id", e.target.value)}
            />
          )}
          <Input
            type="number"
            placeholder="SNMP port (161)"
            onChange={(e) =>
              set("snmp_port", e.target.value ? Number(e.target.value) : undefined)
            }
          />
          <Input
            placeholder="Tags (comma separated)"
            defaultValue={form.tags?.join(", ")}
            onChange={(e) =>
              set(
                "tags",
                e.target.value
                  .split(",")
                  .map((t) => t.trim())
                  .filter(Boolean),
              )
            }
          />
          {save.isError && (
            <div className="text-sm text-red-500">
              {(save.error as Error).message}
            </div>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? "Saving…" : existing ? "Save" : "Add device"}
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
