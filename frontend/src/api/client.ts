// API client (doc 14): Bearer from in-memory store, silent refresh-on-401 via
// the httpOnly cookie, error envelope decoding (doc 23 §5).
import { useAuthStore } from "../features/auth/store";

export class ApiError extends Error {
  code: string;
  status: number;
  traceId?: string;
  constructor(status: number, code: string, message: string, traceId?: string) {
    super(message);
    this.status = status;
    this.code = code;
    this.traceId = traceId;
  }
}

let refreshing: Promise<boolean> | null = null;

async function tryRefresh(): Promise<boolean> {
  refreshing ??= (async () => {
    try {
      const res = await fetch("/api/v1/auth/refresh", { method: "POST" });
      if (!res.ok) return false;
      const body = await res.json();
      useAuthStore.getState().setSession(body.access_token, body.user);
      return true;
    } catch {
      return false;
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

export async function api<T>(
  path: string,
  init: RequestInit = {},
  retry = true,
): Promise<T> {
  const token = useAuthStore.getState().accessToken;
  const headers = new Headers(init.headers);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (init.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`/api/v1${path}`, { ...init, headers });
  if (res.status === 401 && retry && !path.startsWith("/auth/")) {
    if (await tryRefresh()) return api<T>(path, init, false);
    useAuthStore.getState().clear();
  }
  if (res.status === 204) return undefined as T;
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    const err = body?.error;
    throw new ApiError(
      res.status,
      err?.code ?? "unknown",
      err?.message ?? `request failed (${res.status})`,
      err?.trace_id,
    );
  }
  return body as T;
}
