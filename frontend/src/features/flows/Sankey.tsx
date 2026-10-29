// Two-column Sankey for a two-field grouping: left values, right values, and
// a band for each pair whose width is its share of the traffic. Drawn by hand
// in SVG — a bipartite layout is a few lines, and a general Sankey library
// would be a dependency for the one shape the page draws.
import { useMemo, useState } from "react";

export interface SankeyLink {
  left: string;
  right: string;
  value: number;
}

interface Props {
  links: SankeyLink[];
  leftLabel: (k: string) => string;
  rightLabel: (k: string) => string;
  format: (v: number) => string;
  onPick?: (side: "left" | "right", key: string) => void;
}

const palette = ["#0ea5e9", "#f59e0b", "#22c55e", "#a78bfa", "#ef4444", "#14b8a6", "#f472b6", "#eab308"];

export function Sankey({ links, leftLabel, rightLabel, format, onPick }: Props) {
  const [hover, setHover] = useState<number | null>(null);
  const layout = useMemo(() => {
    const height = 360;
    const gap = 6;
    const sum = (side: "left" | "right") => {
      const m = new Map<string, number>();
      for (const l of links) m.set(l[side], (m.get(l[side]) ?? 0) + l.value);
      return [...m.entries()].sort((a, b) => b[1] - a[1]);
    };
    const lefts = sum("left");
    const rights = sum("right");
    const total = links.reduce((a, l) => a + l.value, 0) || 1;
    const place = (nodes: [string, number][]) => {
      const usable = height - gap * Math.max(0, nodes.length - 1);
      let y = 0;
      const pos = new Map<string, { y: number; h: number; used: number }>();
      for (const [k, v] of nodes) {
        const h = Math.max(2, (v / total) * usable);
        pos.set(k, { y, h, used: 0 });
        y += h + gap;
      }
      return { pos, height: Math.max(height, y - gap) };
    };
    const L = place(lefts);
    const R = place(rights);
    const colour = new Map(lefts.map(([k], i) => [k, palette[i % palette.length]]));
    const bands = [...links]
      .sort((a, b) => b.value - a.value)
      .map((l) => {
        const lp = L.pos.get(l.left)!;
        const rp = R.pos.get(l.right)!;
        const w = Math.max(1, (l.value / total) * (height - gap * Math.max(0, lefts.length - 1)));
        const y0 = lp.y + lp.used + w / 2;
        const y1 = rp.y + rp.used + w / 2;
        lp.used += w;
        rp.used += w;
        return { ...l, y0, y1, w, colour: colour.get(l.left)! };
      });
    return { lefts, rights, L, R, bands, h: Math.max(L.height, R.height), colour };
  }, [links]);

  const W = 1000;
  const nodeW = 10;
  const x0 = 230;
  const x1 = W - 230;
  return (
    <svg
      viewBox={`0 0 ${W} ${layout.h + 10}`}
      className="h-auto w-full"
      role="img"
      aria-label="Traffic between the two grouped fields"
    >
      {layout.bands.map((b, i) => (
        <path
          key={`${b.left}>${b.right}`}
          d={`M${x0 + nodeW},${b.y0 + 5} C${(x0 + x1) / 2},${b.y0 + 5} ${(x0 + x1) / 2},${b.y1 + 5} ${x1},${b.y1 + 5}`}
          fill="none"
          stroke={b.colour}
          strokeWidth={b.w}
          strokeOpacity={hover === null ? 0.35 : hover === i ? 0.7 : 0.12}
          onMouseEnter={() => setHover(i)}
          onMouseLeave={() => setHover(null)}
        >
          <title>{`${leftLabel(b.left)} → ${rightLabel(b.right)}: ${format(b.value)}`}</title>
        </path>
      ))}
      {layout.lefts.map(([k, v]) => {
        const p = layout.L.pos.get(k)!;
        return (
          <g key={`l${k}`} className={onPick ? "cursor-pointer" : undefined} onClick={() => onPick?.("left", k)}>
            <rect x={x0} y={p.y + 5} width={nodeW} height={p.h} fill={layout.colour.get(k)} />
            <text x={x0 - 8} y={p.y + 5 + p.h / 2} textAnchor="end" dominantBaseline="middle" className="fill-slate-700 text-[13px] dark:fill-slate-200">
              {truncate(leftLabel(k))} <tspan className="fill-slate-500 dark:fill-slate-400">{format(v)}</tspan>
            </text>
          </g>
        );
      })}
      {layout.rights.map(([k, v]) => {
        const p = layout.R.pos.get(k)!;
        return (
          <g key={`r${k}`} className={onPick ? "cursor-pointer" : undefined} onClick={() => onPick?.("right", k)}>
            <rect x={x1} y={p.y + 5} width={nodeW} height={p.h} className="fill-slate-400 dark:fill-slate-500" />
            <text x={x1 + nodeW + 8} y={p.y + 5 + p.h / 2} dominantBaseline="middle" className="fill-slate-700 text-[13px] dark:fill-slate-200">
              {truncate(rightLabel(k))} <tspan className="fill-slate-500 dark:fill-slate-400">{format(v)}</tspan>
            </text>
          </g>
        );
      })}
    </svg>
  );
}

function truncate(s: string) {
  return s.length > 26 ? s.slice(0, 25) + "…" : s;
}
