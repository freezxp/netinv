// On a cold load (hard refresh, new tab) the in-memory access token is gone
// but the httpOnly refresh cookie may still be valid — try one silent refresh
// before deciding the user is logged out (doc 07 §4, doc 30 §1).
//
// Single-flight at module scope: React StrictMode double-invokes effects, and
// two concurrent /auth/refresh calls with the same rotating token trip reuse
// detection and revoke the whole family (doc 07 §4). One attempt per load.
import { useEffect, useState } from "react";
import { useAuthStore } from "./store";

let attempt: Promise<void> | null = null;

function restoreOnce(): Promise<void> {
  attempt ??= (async () => {
    try {
      const res = await fetch("/api/v1/auth/refresh", { method: "POST" });
      if (res.ok) {
        const body = await res.json();
        useAuthStore.getState().setSession(body.access_token, body.user);
      }
    } catch {
      /* offline or no cookie — fall through to login */
    }
  })();
  return attempt;
}

export function useSessionRestore(): boolean {
  const [done, setDone] = useState(useAuthStore.getState().user !== null);

  useEffect(() => {
    if (useAuthStore.getState().user !== null) {
      setDone(true);
      return;
    }
    let cancelled = false;
    restoreOnce().finally(() => {
      if (!cancelled) setDone(true);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  return done;
}
