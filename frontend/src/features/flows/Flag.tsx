// A country's flag beside its name. SVGs from flag-icons (MIT) rather than
// emoji: Windows renders flag emoji as two bare letters. The glob emits each
// flag as its own asset, so a browser fetches only the flags on screen.
const flags = import.meta.glob<string>("../../../node_modules/flag-icons/flags/4x3/*.svg", {
  query: "?url&no-inline",
  import: "default",
  eager: true,
});

const byCode = new Map(
  Object.entries(flags).map(([path, url]) => [path.slice(path.lastIndexOf("/") + 1, -4).toUpperCase(), url]),
);

/** The flag's asset URL for an ISO 3166 code, or undefined for none. */
export function flagUrl(code: string): string | undefined {
  return code ? byCode.get(code.toUpperCase()) : undefined;
}

export function Flag({ code }: { code: string }) {
  const url = flagUrl(code);
  if (!url) return null;
  return (
    <img
      src={url}
      alt=""
      loading="lazy"
      className="mr-1.5 inline-block h-3 w-4 rounded-[2px] align-[-1px] shadow-[0_0_0_1px_rgba(0,0,0,0.12)]"
    />
  );
}
