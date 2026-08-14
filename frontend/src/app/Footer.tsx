// Build provenance, visible from any page: which version is running and where
// the source for it lives. NetInv is Apache-2.0 and public (ADR-019), so the
// repository link is a genuine destination for whoever is looking at the
// screen, not decoration.
export const REPO_URL = "https://github.com/freezxp/netinv";

export function Footer({ className }: { className?: string }) {
  return (
    <footer
      className={
        "flex flex-wrap items-center justify-center gap-x-2 gap-y-1 " +
        "text-xs text-slate-500 dark:text-slate-500 " +
        (className ?? "")
      }
    >
      <span>
        NetInv <span className="mono">{__APP_VERSION__}</span>
      </span>
      <span aria-hidden="true">·</span>
      <a
        href={REPO_URL}
        target="_blank"
        // noreferrer alongside noopener because this is an authenticated page:
        // the referrer would otherwise leak the deployment's hostname.
        rel="noopener noreferrer"
        className="hover:text-sky-500 hover:underline"
      >
        github.com/freezxp/netinv
      </a>
    </footer>
  );
}
