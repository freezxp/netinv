// What an exported PDF report looks like (doc 30 §8).
//
// Deployment configuration rather than a per-export option, for both halves of
// it. The company running NetInv is the company on every page it hands a
// customer, and the columns a customer-facing page carries are a decision made
// once — not one an operator should re-make, differently, on every export.
//
// The preview is the point of the page. Every control here changes a document
// nobody sees until it has been mailed to a customer, so the form renders the
// real thing, through the real server-side renderer, from the values in the
// form rather than from what was last saved.
import { useEffect, useState } from "react";
import {
  useBranding,
  useReportColumns,
  useSaveBranding,
} from "../../api/hooks";
import type { Branding } from "../../api/hooks";
import { useAuthStore } from "../auth/store";
import { Button, Card, Input, Select } from "../../components/ui";

const BLANK: Branding = {
  company: "",
  subtitle: "",
  logo: "",
  footer: "",
  accent: "",
};

// Matches the server's cap. Checked here too so someone dragging in a press-kit
// PNG is told before a megabyte of base64 goes over the wire to be rejected.
const LOGO_LIMIT = 512 * 1024;

export function ReportStyleTab() {
  const current = useBranding();
  const catalogue = useReportColumns();
  const save = useSaveBranding();
  const [form, setForm] = useState<Branding | null>(null);
  const [logoError, setLogoError] = useState("");
  const b = form ?? current.data ?? BLANK;
  const set = (patch: Partial<Branding>) => setForm({ ...b, ...patch });

  const all = catalogue.data?.columns ?? [];
  const chosen = b.columns ?? catalogue.data?.default ?? [];
  const titleOf = (key: string) => all.find((c) => c.key === key)?.title ?? key;

  // Read as a data URI in the browser: the logo travels with the rest of the
  // branding as one JSON body, so there is no upload endpoint, no file on a
  // container filesystem that vanishes when it is replaced, and nothing to
  // clean up when it changes.
  function pickLogo(file: File) {
    setLogoError("");
    if (file.size > LOGO_LIMIT) {
      setLogoError(
        `That file is ${Math.round(file.size / 1024)} KB; the limit is ${LOGO_LIMIT / 1024} KB.`,
      );
      return;
    }
    const fr = new FileReader();
    fr.onload = () => set({ logo: String(fr.result) });
    fr.onerror = () => setLogoError("That file could not be read.");
    fr.readAsDataURL(file);
  }

  function toggleColumn(key: string) {
    if (chosen.includes(key)) {
      // The last column cannot be removed: an empty list falls back to the
      // default on the server, which would silently undo the whole choice.
      if (chosen.length === 1) return;
      set({ columns: chosen.filter((k) => k !== key) });
      return;
    }
    set({ columns: [...chosen, key] });
  }

  function move(key: string, by: number) {
    const i = chosen.indexOf(key);
    const j = i + by;
    if (i < 0 || j < 0 || j >= chosen.length) return;
    const next = [...chosen];
    [next[i], next[j]] = [next[j], next[i]];
    set({ columns: next });
  }

  return (
    <div className="space-y-4">
      <Card title="Report identity">
        <p className="text-sm text-slate-500">
          Printed on the header and footer of every PDF report. CSV exports are
          unaffected — they go to a spreadsheet, not to a customer. Leave it
          empty and exports say “NetInv”.
        </p>

        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          <label className="text-xs">
            <span className="block text-slate-500">Company</span>
            <Input
              value={b.company}
              maxLength={200}
              onChange={(e) => set({ company: e.target.value })}
              placeholder="Acme Networks"
            />
          </label>
          <label className="text-xs">
            <span className="block text-slate-500">Subtitle</span>
            <Input
              value={b.subtitle}
              maxLength={200}
              onChange={(e) => set({ subtitle: e.target.value })}
              placeholder="Network Operations"
            />
          </label>
          <label className="text-xs sm:col-span-2">
            <span className="block text-slate-500">
              Footer — printed on every page
            </span>
            <Input
              value={b.footer}
              maxLength={200}
              onChange={(e) => set({ footer: e.target.value })}
              placeholder="Commercial in confidence"
            />
          </label>
          <label className="text-xs">
            <span className="block text-slate-500">Accent colour</span>
            <div className="flex items-center gap-2">
              <input
                type="color"
                value={
                  /^#[0-9a-fA-F]{6}$/.test(b.accent) ? b.accent : "#334155"
                }
                onChange={(e) => set({ accent: e.target.value })}
                className="h-8 w-10 rounded border border-slate-300 bg-transparent dark:border-slate-700"
              />
              <Input
                value={b.accent}
                onChange={(e) => set({ accent: e.target.value })}
                placeholder="#334155"
              />
            </div>
          </label>
          <div className="text-xs">
            <span className="block text-slate-500">
              Logo — PNG, JPEG or GIF
            </span>
            <div className="flex items-center gap-2">
              <input
                type="file"
                accept="image/png,image/jpeg,image/gif"
                className="text-xs"
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  if (f) pickLogo(f);
                }}
              />
              {b.logo && (
                <Button variant="ghost" onClick={() => set({ logo: "" })}>
                  Remove
                </Button>
              )}
            </div>
            {b.logo && (
              <img
                src={b.logo}
                alt="Report logo"
                className="mt-2 max-h-14 rounded border border-slate-200 bg-white p-1 dark:border-slate-700"
              />
            )}
          </div>
        </div>
        {logoError && (
          <div className="mt-2 text-sm text-rose-600">{logoError}</div>
        )}
      </Card>

      <Card title="Report layout">
        <p className="text-sm text-slate-500">
          The columns of the per-interface table, and whether each row is
          followed by a chart of what it carried. A report grouped by customer
          keeps its own columns — one row per customer, with no interface to
          name — but follows the chart setting.
        </p>

        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          <label className="text-xs">
            <span className="block text-slate-500">Charts</span>
            <Select
              value={b.charts ?? "each"}
              onChange={(e) =>
                set({ charts: e.target.value as Branding["charts"] })
              }
            >
              <option value="each">One chart per row</option>
              <option value="none">Table only</option>
            </Select>
          </label>
          <label className="text-xs">
            <span className="block text-slate-500">Chart size</span>
            <Select
              value={b.chart_size ?? "compact"}
              disabled={(b.charts ?? "each") === "none"}
              onChange={(e) =>
                set({ chart_size: e.target.value as Branding["chart_size"] })
              }
            >
              <option value="compact">Two to a row</option>
              <option value="full">Full width</option>
            </Select>
          </label>
        </div>

        <div className="mt-4 text-xs">
          <span className="block text-slate-500">
            Columns — ticked ones print, in this order
          </span>
          <ul className="mt-1 divide-y divide-slate-100 rounded border border-slate-200 dark:divide-slate-800 dark:border-slate-700">
            {[
              ...chosen,
              ...all.map((c) => c.key).filter((k) => !chosen.includes(k)),
            ].map((key) => {
              const on = chosen.includes(key);
              const i = chosen.indexOf(key);
              return (
                <li key={key} className="flex items-center gap-2 px-2 py-1">
                  <input
                    type="checkbox"
                    checked={on}
                    disabled={on && chosen.length === 1}
                    onChange={() => toggleColumn(key)}
                  />
                  <span className={on ? "" : "text-slate-400"}>
                    {titleOf(key)}
                  </span>
                  {on && (
                    <span className="ml-auto flex gap-1">
                      <Button
                        variant="ghost"
                        disabled={i === 0}
                        onClick={() => move(key, -1)}
                      >
                        ↑
                      </Button>
                      <Button
                        variant="ghost"
                        disabled={i === chosen.length - 1}
                        onClick={() => move(key, 1)}
                      >
                        ↓
                      </Button>
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
          <p className="mt-1 text-slate-500">
            The default names an interface by its alias alone: the device name
            and the ifName are internal topology, and a page that goes to a
            customer should not carry which chassis their circuit lands on.
          </p>
        </div>
      </Card>

      {save.isError && (
        <div className="text-sm text-rose-600">
          {(save.error as Error).message}
        </div>
      )}
      {save.isSuccess && !form && (
        <div className="text-sm text-emerald-600">Saved.</div>
      )}

      <div className="flex items-center gap-2">
        <Button
          disabled={save.isPending || !form}
          onClick={() =>
            save
              .mutateAsync(b)
              .then(() => setForm(null))
              .catch(() => {})
          }
        >
          Save
        </Button>
        <Button variant="ghost" disabled={!form} onClick={() => setForm(null)}>
          Discard
        </Button>
      </div>

      <PreviewCard brand={b} unsaved={form !== null} />
    </div>
  );
}

// PreviewCard renders the real document from the values in the form, saved or
// not. Sample traffic rather than live: the sitting where someone configures
// this is usually before the deployment has collected anything, and a preview
// built from real numbers is a real report that happens to be unsaved — which
// is exactly the document that ends up attached to an email.
function PreviewCard({
  brand,
  unsaved,
}: {
  brand: Branding;
  unsaved: boolean;
}) {
  const [url, setUrl] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  // Blob URLs are held by the document until revoked, so the previous one goes
  // when it is replaced and the last one goes when the tab does.
  useEffect(
    () => () => {
      if (url) URL.revokeObjectURL(url);
    },
    [url],
  );

  async function render() {
    setBusy(true);
    setError("");
    try {
      const res = await fetch("/api/v1/reports/preview", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${useAuthStore.getState().accessToken}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify(brand),
      });
      if (!res.ok) {
        const body = await res.json().catch(() => null);
        setError(body?.error?.message ?? `Preview failed (${res.status}).`);
        return;
      }
      const next = URL.createObjectURL(await res.blob());
      setUrl((prev) => {
        if (prev) URL.revokeObjectURL(prev);
        return next;
      });
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Preview">
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={render} disabled={busy}>
          {busy ? "Rendering…" : url ? "Refresh preview" : "Render preview"}
        </Button>
        {url && (
          // Not every browser will frame a PDF, so the bytes are also offered
          // as a file. A preview nobody can see is not a preview.
          <a
            href={url}
            download="netinv-report-preview.pdf"
            className="text-sm text-sky-600 hover:underline"
          >
            Download instead
          </a>
        )}
        <span className="text-xs text-slate-500">
          Sample data, {unsaved ? "including your unsaved changes" : "as saved"}
          .
        </span>
      </div>
      {error && <div className="mt-2 text-sm text-rose-600">{error}</div>}
      {url && (
        <iframe
          src={url}
          title="Report preview"
          className="mt-3 h-[36rem] w-full rounded border border-slate-200 dark:border-slate-700"
        />
      )}
    </Card>
  );
}
