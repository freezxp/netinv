import { type FormEvent, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useLogin } from "../../api/hooks";
import { useAuthStore } from "./store";
import { Button, Card, Input } from "../../components/ui";
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
    <div className="flex h-full items-center justify-center">
      <Card className="w-80">
        <div className="mb-4 text-center">
          <div className="text-2xl font-bold text-sky-500">NetInv</div>
          <div className="text-sm text-slate-500">Network asset monitoring</div>
        </div>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <Input
            placeholder="Username"
            autoComplete="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoFocus
          />
          <Input
            type="password"
            placeholder="Password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          {errorText && <div className="text-sm text-red-500">{errorText}</div>}
          <Button type="submit" disabled={login.isPending || !username || !password}>
            {login.isPending ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      </Card>
    </div>
  );
}
