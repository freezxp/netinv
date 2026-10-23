import { type FormEvent, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useLogin } from "../../api/hooks";
import { useAuthStore } from "./store";
import { Button, Input } from "../../components/ui";
import { Footer } from "../../app/Footer";
import { ApiError } from "../../api/client";

export function LoginPage() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const login = useLogin();
  const user = useAuthStore((s) => s.user);
  const navigate = useNavigate();

  useEffect(() => {
    if (user) navigate("/", { replace: true });
  }, [user, navigate]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    login.mutate({ username, password });
  };

  // Generic failure text — no user enumeration (doc 30 §1).
  const errorText =
    login.error instanceof ApiError
      ? login.error.code === "account_locked"
        ? "Account temporarily locked. Try again in a few minutes."
        : "Sign-in failed. Check your username and password."
      : login.error
        ? "Something went wrong. Try again."
        : null;

  return (
    // Laid out like PICA's sign-in: a panel in the deployment accent carrying
    // the product name, beside the form. The version stays on this page: it is
    // the one page reachable without credentials, which makes it where you
    // look when asking what a deployment is actually running.
    <div className="flex h-full">
      <div
        className="relative hidden flex-1 flex-col justify-end p-12 text-white md:flex"
        style={{
          background:
            "radial-gradient(circle at 20% 15%, var(--color-sky-500), transparent 60%), linear-gradient(135deg, var(--color-sky-600), var(--color-sky-800))",
        }}
      >
        <div className="font-mono text-3xl font-semibold tracking-[-0.01em]">NetInv</div>
        <div className="mt-3 text-lg text-white/80">Network asset monitoring</div>
      </div>
      <div className="flex flex-1 flex-col items-center justify-center gap-6 bg-[var(--bg)] px-4">
        <form onSubmit={submit} className="flex w-full max-w-[384px] flex-col">
          <div className="mb-1 font-mono text-xl font-semibold md:hidden">NetInv</div>
          <h1 className="mb-6 text-2xl font-semibold tracking-[-0.015em]">Sign in</h1>
          <label className="eyebrow mb-1" htmlFor="login-username">
            Username
          </label>
          <Input
            id="login-username"
            autoComplete="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoFocus
            className="mb-5 py-2"
          />
          <label className="eyebrow mb-1" htmlFor="login-password">
            Password
          </label>
          <Input
            id="login-password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="mb-4 py-2"
          />
          {errorText && <div className="mb-3 text-sm text-red-600 dark:text-red-400">{errorText}</div>}
          <Button
            type="submit"
            className="py-2"
            disabled={login.isPending || !username || !password}
          >
            {login.isPending ? "Signing in…" : "Sign in"}
          </Button>
        </form>
        <Footer />
      </div>
    </div>
  );
}
