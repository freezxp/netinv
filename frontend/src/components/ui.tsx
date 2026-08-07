// Shared UI primitives (doc 30 §0 tokens). Deliberately small; grows with the
// feature sprints.
import type {
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
} from "react";

export function cx(...parts: Array<string | false | undefined>) {
  return parts.filter(Boolean).join(" ");
}

export function Button({
  variant = "primary",
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "ghost" | "danger";
}) {
  return (
    <button
      className={cx(
        "rounded-md px-3 py-1.5 text-sm font-medium transition-colors disabled:opacity-50",
        variant === "primary" &&
          "bg-sky-600 text-white hover:bg-sky-500 dark:bg-sky-600 dark:hover:bg-sky-500",
        variant === "ghost" &&
          "text-slate-600 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-800",
        variant === "danger" && "bg-red-600 text-white hover:bg-red-500",
        className,
      )}
      {...props}
    />
  );
}

export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...props}
      className={cx(
        "rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm outline-none",
        "focus:border-sky-500 focus:ring-1 focus:ring-sky-500",
        "dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200",
        props.className,
      )}
    />
  );
}

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      {...props}
      className={cx(
        "rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm outline-none",
        "focus:border-sky-500 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200",
        props.className,
      )}
    />
  );
}

export function Card({
  title,
  children,
  className,
}: {
  title?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cx(
        "rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900",
        className,
      )}
    >
      {title && (
        <div className="mb-3 text-sm font-semibold text-slate-500 dark:text-slate-400">
          {title}
        </div>
      )}
      {children}
    </div>
  );
}

const statusColor: Record<string, string> = {
  active: "var(--status-ok)",
  up: "var(--status-ok)",
  ok: "var(--status-ok)",
  pending: "var(--status-info)",
  unreachable: "var(--status-unreachable)",
  disabled: "var(--status-muted)",
  retired: "var(--status-muted)",
  warning: "var(--status-warning)",
  critical: "var(--status-critical)",
  firing: "var(--status-critical)",
};

// Status is always color + text, never color alone (accessibility, doc 30 §13).
export function StatusBadge({ status }: { status: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-sm">
      <span
        aria-hidden
        className="inline-block h-2 w-2 rounded-full"
        style={{ background: statusColor[status] ?? "var(--status-muted)" }}
      />
      {status}
    </span>
  );
}

export function SeverityPill({ severity }: { severity: string }) {
  return (
    <span
      className="rounded px-1.5 py-0.5 text-xs font-semibold uppercase text-white"
      style={{ background: statusColor[severity] ?? "var(--status-muted)" }}
    >
      {severity}
    </span>
  );
}

export function EmptyState({ children }: { children: ReactNode }) {
  return (
    <div className="py-10 text-center text-sm text-slate-500 dark:text-slate-400">
      {children}
    </div>
  );
}
