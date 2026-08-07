// User management (doc 30 §11, Admin only).
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import {
  Button,
  Card,
  EmptyState,
  Input,
  Select,
  StatusBadge,
} from "../../components/ui";

interface UserRow {
  id: string;
  username: string;
  email: string;
  display_name: string;
  status: string;
  roles: string[];
  last_login_at: string | null;
}

export function UsersPage() {
  const qc = useQueryClient();
  const users = useQuery({
    queryKey: ["users"],
    queryFn: () => api<{ data: UserRow[] }>("/users"),
  });
  const [form, setForm] = useState({
    username: "",
    email: "",
    display_name: "",
    role: "role_readonly",
  });
  const [oneTime, setOneTime] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () =>
      api<{ temporary_password?: string }>("/users", {
        method: "POST",
        body: JSON.stringify({
          username: form.username,
          email: form.email,
          display_name: form.display_name,
          roles: [form.role],
        }),
      }),
    onSuccess: (r) => {
      setOneTime(r.temporary_password ?? null);
      qc.invalidateQueries({ queryKey: ["users"] });
    },
  });
  const action = useMutation({
    mutationFn: ({ id, verb }: { id: string; verb: string }) =>
      api<{ temporary_password?: string }>(`/users/${id}/${verb}`, {
        method: "POST",
      }),
    onSuccess: (r) => {
      if (r?.temporary_password) setOneTime(r.temporary_password);
      qc.invalidateQueries({ queryKey: ["users"] });
    },
  });

  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-3">
      <h1 className="text-xl font-semibold">Users</h1>
      <Card title="Create user">
        <div className="flex flex-wrap gap-2">
          <Input
            placeholder="username"
            value={form.username}
            onChange={(e) => setForm({ ...form, username: e.target.value })}
          />
          <Input
            placeholder="email"
            value={form.email}
            onChange={(e) => setForm({ ...form, email: e.target.value })}
          />
          <Input
            placeholder="display name"
            value={form.display_name}
            onChange={(e) => setForm({ ...form, display_name: e.target.value })}
          />
          <Select
            value={form.role}
            onChange={(e) => setForm({ ...form, role: e.target.value })}
          >
            <option value="role_admin">admin</option>
            <option value="role_operator">operator</option>
            <option value="role_readonly">readonly</option>
            <option value="role_auditor">auditor</option>
          </Select>
          <Button
            disabled={!form.username || !form.email || !form.display_name}
            onClick={() => create.mutate()}
          >
            Create
          </Button>
        </div>
        {create.isError && (
          <div className="mt-2 text-sm text-red-500">
            {(create.error as Error).message}
          </div>
        )}
        {oneTime && (
          <div className="mono mt-2 rounded bg-amber-500/10 p-2 text-xs">
            Temporary password (shown once):{" "}
            <span className="select-all">{oneTime}</span>
          </div>
        )}
      </Card>
      <Card className="p-0">
        <table className="w-full text-sm">
          <tbody>
            {users.data?.data.map((u) => (
              <tr
                key={u.id}
                className="border-b border-slate-100 dark:border-slate-800/60"
              >
                <td className="px-4 py-2">
                  <StatusBadge status={u.status} />
                </td>
                <td className="px-4 py-2 font-medium">{u.username}</td>
                <td className="px-4 py-2 text-slate-500">{u.display_name}</td>
                <td className="px-4 py-2 text-slate-500">
                  {u.roles.join(", ")}
                </td>
                <td className="px-4 py-2 text-xs text-slate-500">
                  {u.last_login_at
                    ? `last login ${new Date(u.last_login_at).toLocaleString()}`
                    : "never signed in"}
                </td>
                <td className="px-4 py-2 text-right">
                  <Button
                    variant="ghost"
                    onClick={() =>
                      action.mutate({ id: u.id, verb: "reset-password" })
                    }
                  >
                    Reset PW
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() =>
                      action.mutate({
                        id: u.id,
                        verb:
                          u.status === "deactivated" ? "activate" : "deactivate",
                      })
                    }
                  >
                    {u.status === "deactivated" ? "Activate" : "Deactivate"}
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {users.data?.data.length === 0 && <EmptyState>No users.</EmptyState>}
      </Card>
    </div>
  );
}
