// uPlot wrapper (doc 14: one wrapper so the chart lib stays swappable).
// Takes Prometheus range-matrix results and renders aligned series.
import { useEffect, useMemo, useRef } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";

export interface PromMatrix {
  metric: Record<string, string>;
  values: Array<[number, string]>;
}

const palette = [
  "#0ea5e9",
  "#f59e0b",
  "#22c55e",
  "#a78bfa",
  "#ef4444",
  "#14b8a6",
  "#f472b6",
  "#eab308",
];

interface Props {
  result: PromMatrix[];
  height?: number;
  label?: (metric: Record<string, string>) => string;
  format?: (v: number) => string;
}

export function TimeSeries({ result, height = 220, label, format }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

  // Align all series on the union of timestamps (fixed-step queries align 1:1).
  const { data, names } = useMemo(() => {
    const tsSet = new Set<number>();
    for (const s of result) for (const [t] of s.values) tsSet.add(t);
    const xs = [...tsSet].sort((a, b) => a - b);
    const idx = new Map(xs.map((t, i) => [t, i]));
    const series = result.map((s) => {
      const ys: Array<number | null> = new Array(xs.length).fill(null);
      for (const [t, v] of s.values) ys[idx.get(t)!] = parseFloat(v);
      return ys;
    });
    const names = result.map(
      (s, i) => label?.(s.metric) ?? s.metric.device ?? `series ${i + 1}`,
    );
    return { data: [xs, ...series] as uPlot.AlignedData, names };
  }, [result, label]);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const dark = document.documentElement.classList.contains("dark");
    const axisColor = dark ? "#94a3b8" : "#475569";
    const gridColor = dark ? "#1e293b" : "#e2e8f0";

    const make = () => {
      plotRef.current?.destroy();
      plotRef.current = new uPlot(
        {
          width: el.clientWidth || 600,
          height,
          legend: { show: names.length <= 6 },
          series: [
            {},
            ...names.map((name, i) => ({
              label: name,
              stroke: palette[i % palette.length],
              width: 1.5,
              value: (_u: uPlot, v: number | null) =>
                v == null ? "—" : (format?.(v) ?? v.toPrecision(4)),
            })),
          ],
          axes: [
            { stroke: axisColor, grid: { stroke: gridColor }, ticks: { stroke: gridColor } },
            {
              stroke: axisColor,
              grid: { stroke: gridColor },
              ticks: { stroke: gridColor },
              size: 60,
              values: (_u, vals) =>
                vals.map((v) => (format ? format(v) : String(v))),
            },
          ],
          cursor: { drag: { x: false, y: false } },
        },
        data,
        el,
      );
    };
    make();
    const onResize = () => {
      if (plotRef.current && el.clientWidth) {
        plotRef.current.setSize({ width: el.clientWidth, height });
      }
    };
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      plotRef.current?.destroy();
      plotRef.current = null;
    };
  }, [data, names, height, format]);

  if (result.length === 0) {
    return (
      <div
        className="flex items-center justify-center text-sm text-slate-500"
        style={{ height }}
      >
        No data for this range
      </div>
    );
  }
  return <div ref={ref} />;
}
