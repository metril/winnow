import { Fragment } from "react";
import {
  Area, ComposedChart, Line, CartesianGrid, ResponsiveContainer,
  Tooltip, XAxis, YAxis, AreaChart,
} from "recharts";
import { fmt, shortTs, tsMs } from "../util";
import { HeatCell } from "../api";
import { useChartTheme } from "./chartTheme";

const axisFmt = (t: number) => shortTs(new Date(t).toISOString()).slice(5, 16);

interface Pt { bucket: string; value: number }
type SeriesMap = Record<string, Pt[]>;

function mergeRows(maps: { key: string; pts: { bucket: string; value: number }[] }[]) {
  const rows = new Map<number, any>();
  maps.forEach(({ key, pts }) => pts.forEach((p) => {
    const t = tsMs(p.bucket);
    const row = rows.get(t) || { t };
    row[key] = p.value;
    rows.set(t, row);
  }));
  return [...rows.values()].sort((a, b) => a.t - b.t);
}

function ChipLegend({ items }: { items: { key: string; label: string; color: string }[] }) {
  return (
    <div className="mb-2 flex flex-wrap gap-x-4 gap-y-1">
      {items.map((it) => (
        <span key={it.key} className="inline-flex items-center gap-1.5 text-micro text-secondary">
          <span className="h-2 w-2 rounded-full" style={{ background: it.color }} />{it.label}
        </span>
      ))}
    </div>
  );
}

// Sparkline — tiny gradient area, no axes (for StatCards and the hero).
export function Sparkline({ data, color, height = 36 }: { data: number[]; color?: string; height?: number }) {
  const t = useChartTheme();
  const c = color || t.brand;
  const id = "sg" + c.replace(/[^a-z0-9]/gi, "");
  const d = data.map((v, i) => ({ i, v }));
  return (
    <ResponsiveContainer width="100%" height={height}>
      <AreaChart data={d} margin={{ top: 2, bottom: 0, left: 0, right: 0 }}>
        <defs><linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={c} stopOpacity={0.35} /><stop offset="100%" stopColor={c} stopOpacity={0} />
        </linearGradient></defs>
        <Area dataKey="v" stroke={c} strokeWidth={1.5} fill={`url(#${id})`} dot={false} isAnimationActive={false} />
      </AreaChart>
    </ResponsiveContainer>
  );
}

export function MultiSeriesChart({ data, labels, height = 240 }:
  { data: SeriesMap; labels?: Record<string, string>; height?: number }) {
  const th = useChartTheme();
  const keys = Object.keys(data);
  const merged = mergeRows(keys.map((k) => ({ key: k, pts: data[k] })));
  return (
    <>
      <ChipLegend items={keys.map((k, i) => ({ key: k, label: labels?.[k] || `#${k}`, color: th.palette[i % th.palette.length] }))} />
      <ResponsiveContainer width="100%" height={height}>
        <ComposedChart data={merged}>
          <CartesianGrid {...th.gridProps} />
          <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={axisFmt} {...th.axisX} />
          <YAxis tickFormatter={(v) => fmt(v)} {...th.axisY} />
          <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={th.tooltipStyle} />
          {keys.map((k, i) => (
            <Line key={k} type="monotone" dataKey={k} name={labels?.[k] || k} stroke={th.palette[i % th.palette.length]} dot={false} strokeWidth={2} isAnimationActive={false} />
          ))}
        </ComposedChart>
      </ResponsiveContainer>
    </>
  );
}

export function OverlayChart({ reference, meters, labels, height = 260 }:
  { reference: Pt[]; meters: SeriesMap; labels?: Record<string, string>; height?: number }) {
  const th = useChartTheme();
  const keys = Object.keys(meters);
  const merged = mergeRows([{ key: "__plug", pts: reference }, ...keys.map((k) => ({ key: k, pts: meters[k] }))]);
  return (
    <>
      <ChipLegend items={[{ key: "__plug", label: "monitored power", color: th.gold },
        ...keys.map((k, i) => ({ key: k, label: labels?.[k] || `#${k}`, color: th.palette[(i + 1) % th.palette.length] }))]} />
      <ResponsiveContainer width="100%" height={height}>
        <ComposedChart data={merged}>
          <defs><linearGradient id="plugfill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={th.gold} stopOpacity={0.18} /><stop offset="100%" stopColor={th.gold} stopOpacity={0} />
          </linearGradient></defs>
          <CartesianGrid {...th.gridProps} />
          <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={axisFmt} {...th.axisX} />
          <YAxis yAxisId="L" tickFormatter={(v) => fmt(v)} {...th.axisY} stroke={th.gold} />
          <YAxis yAxisId="R" orientation="right" tickFormatter={(v) => fmt(v)} {...th.axisY} stroke={th.brand} />
          <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={th.tooltipStyle} />
          <Area yAxisId="L" dataKey="__plug" name="monitored power (W)" stroke={th.gold} fill="url(#plugfill)" strokeWidth={2} dot={false} isAnimationActive={false} />
          {keys.map((k, i) => (
            <Line yAxisId="R" key={k} type="monotone" dataKey={k} name={labels?.[k] || `meter ${k}`} stroke={th.palette[(i + 1) % th.palette.length]} dot={false} strokeWidth={2} isAnimationActive={false} />
          ))}
        </ComposedChart>
      </ResponsiveContainer>
    </>
  );
}

// ConfidenceBar — a 0..1 correlation as a labelled bar with the shared ramp.
export function ConfidenceBar({ r }: { r: number | null }) {
  const t = useChartTheme();
  if (r === null || r === undefined) return <span className="text-tertiary">–</span>;
  const pct = Math.max(0, Math.min(1, r)) * 100;
  const color = r >= 0.8 ? t.brand : r >= 0.5 ? t.gold : t.faint;
  return (
    <div className="relative h-5 w-full min-w-[88px] overflow-hidden rounded bg-raised" title={`r=${r}`}>
      <div className="h-full rounded" style={{ width: `${pct}%`, background: color, opacity: 0.9 }} />
      <span className="absolute inset-0 grid place-items-center text-micro font-medium tabular-nums text-text">{r.toFixed(2)}</span>
    </div>
  );
}

const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export function Heatmap({ cells }: { cells: HeatCell[] }) {
  const t = useChartTheme();
  const max = Math.max(0.0001, ...cells.map((c) => c.value));
  const grid: Record<string, number> = {};
  cells.forEach((c) => { grid[`${c.dow}-${c.hour}`] = c.value; });
  return (
    <div className="overflow-x-auto">
      <div className="inline-grid gap-[2px]" style={{ gridTemplateColumns: `auto repeat(24, 14px)` }}>
        <div />
        {Array.from({ length: 24 }, (_, h) => <div key={h} className="text-center text-[9px] text-tertiary">{h % 3 === 0 ? h : ""}</div>)}
        {DOW.map((d, dow) => (
          <Fragment key={dow}>
            <div className="pr-2 text-right text-[10px] leading-[14px] text-secondary">{d}</div>
            {Array.from({ length: 24 }, (_, h) => {
              const v = grid[`${dow}-${h}`] || 0;
              const a = v / max;
              return <div key={`${dow}-${h}`} title={`${d} ${h}:00 — ${fmt(v, 2)}`} className="h-[14px] w-[14px] rounded-[2px]"
                style={{ background: v ? t.heat(0.12 + a * 0.8) : t.empty }} />;
            })}
          </Fragment>
        ))}
      </div>
    </div>
  );
}
