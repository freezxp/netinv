// Auth session store: access token lives in memory only (doc 20 §3 — never
// localStorage); the refresh token is an httpOnly cookie the JS can't see.
import { create } from "zustand";
import type { User } from "../../api/types";

interface AuthState {
  accessToken: string | null;
  user: User | null;
  setSession: (token: string, user: User) => void;
  clear: () => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  accessToken: null,
  user: null,
  setSession: (accessToken, user) => set({ accessToken, user }),
  clear: () => set({ accessToken: null, user: null }),
}));

export function hasPermissionRole(user: User | null, ...roles: string[]) {
  if (!user) return false;
  return user.roles.some((r) => r === "admin" || roles.includes(r));
}
