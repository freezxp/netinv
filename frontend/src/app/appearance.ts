// Colour scheme, appearance and deployment accent (doc 30 §0). All are
// applied before React renders, so the sign-in page is themed too and nothing
// flashes in the default colours first.

export type ThemeMode = "light" | "system" | "dark";

const THEME_KEY = "netinv-theme";
const ACCENT_KEY = "netinv-accent";
const media = window.matchMedia?.("(prefers-color-scheme: dark)");

// Storage can throw in a private window or with site data blocked; a
// remembered preference is a nicety, never a reason to fail.
function read(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function write(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* ignore */
  }
}

export function storedTheme(): ThemeMode {
  const v = read(THEME_KEY);
  // Dark stays the default for an unset preference: it is what every
  // existing install has been showing, and a NOC screen usually wants it.
  return v === "light" || v === "system" ? v : "dark";
}

export function applyTheme(mode: ThemeMode) {
  write(THEME_KEY, mode);
  const dark = mode === "dark" || (mode === "system" && !!media?.matches);
  document.documentElement.classList.toggle("dark", dark);
}

media?.addEventListener?.("change", () => {
  if (storedTheme() === "system") applyTheme("system");
});

// --- appearance ---------------------------------------------------------

// Legacy is PICA's classic look; Modern its softer one — rounder corners,
// raised translucent surfaces over a gently lit canvas. Per browser, like the
// colour scheme: it is a matter of taste, not of the deployment.
export type Style = "legacy" | "modern";
const STYLE_KEY = "netinv-style";

export function storedStyle(): Style {
  return read(STYLE_KEY) === "modern" ? "modern" : "legacy";
}

export function applyStyle(style: Style) {
  write(STYLE_KEY, style);
  document.documentElement.dataset.style = style;
}

// Another tab changing it follows here too.
window.addEventListener("storage", (e) => {
  if (e.key === STYLE_KEY || e.key === null) document.documentElement.dataset.style = storedStyle();
});

// --- accent ---------------------------------------------------------------

export const DEFAULT_ACCENT = "#0b6e99";
const HEX = /^#[0-9a-f]{6}$/i;

const rgb = (h: string) =>
  [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16)) as [number, number, number];
const hex = (n: number) =>
  Math.round(Math.max(0, Math.min(255, n))).toString(16).padStart(2, "0");
// a·(1-t) + b·t
function mix(a: string, b: string, t: number) {
  const [r1, g1, b1] = rgb(a);
  const [r2, g2, b2] = rgb(b);
  return `#${hex(r1 + (r2 - r1) * t)}${hex(g1 + (g2 - g1) * t)}${hex(b1 + (b2 - b1) * t)}`;
}

// Eleven shades around one colour, the same derivation PICA uses: 600 is the
// colour itself (the light-scheme accent), 400 a lighter one for dark.
function palette(base: string): Record<number, string> {
  return {
    50: mix(base, "#ffffff", 0.9),
    100: mix(base, "#ffffff", 0.75),
    200: mix(base, "#ffffff", 0.6),
    300: mix(base, "#ffffff", 0.45),
    400: mix(base, "#ffffff", 0.32),
    500: mix(base, "#ffffff", 0.15),
    600: base,
    700: mix(base, "#000000", 0.15),
    800: mix(base, "#000000", 0.3),
    900: mix(base, "#000000", 0.45),
    950: mix(base, "#0e1317", 0.75),
  };
}

/** Re-points the accent scale (Tailwind's `sky`) at one colour, or restores
 *  the default when given anything that is not #rrggbb. */
export function applyAccent(accent: string | undefined | null) {
  const el =
    document.getElementById("netinv-accent") ??
    document.head.appendChild(
      Object.assign(document.createElement("style"), { id: "netinv-accent" }),
    );
  if (!accent || !HEX.test(accent) || accent.toLowerCase() === DEFAULT_ACCENT) {
    el.textContent = "";
    write(ACCENT_KEY, "");
    return;
  }
  const p = palette(accent.toLowerCase());
  const scale = Object.entries(p)
    .map(([k, v]) => `--color-sky-${k}:${v};`)
    .join("");
  el.textContent =
    `:root{${scale}--accent:${p[600]};--accent-ink:${p[700]};--accent-wash:${p[50]};--status-info:${p[600]};}` +
    `.dark{--accent:${p[400]};--accent-ink:${p[300]};--accent-wash:${p[950]};--status-info:${p[400]};}`;
  write(ACCENT_KEY, accent);
}

/** Applies the cached scheme and accent at once, then refreshes the accent
 *  from the server, which an admin may have changed since. */
export function initAppearance() {
  applyTheme(storedTheme());
  applyStyle(storedStyle());
  applyAccent(read(ACCENT_KEY));
  fetch("/api/v1/ui/appearance")
    .then((r) => (r.ok ? r.json() : null))
    .then((a: { accent?: string } | null) => {
      if (a) applyAccent(a.accent);
    })
    .catch(() => {
      /* keep the cached accent */
    });
}
