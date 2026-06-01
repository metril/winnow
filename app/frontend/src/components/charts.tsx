import { Fragment } from "react";
import {
  Area, ComposedChart, Line, CartesianGrid, ResponsiveContainer,
  Tooltip, XAxis, YAxis, Legend,
} from "recharts";
import { fmt, shortTs, tsMs } from "../util";
import { HeatCell } from "../api";

const COLORS = ["#2dd4bf", "#fbbf24", "#a78bfa", "#f87171", "#60a5fa", "#4ade80", "#f472b6", "#fb923c"];
const GRID = "#1e2935", AXIS = "#6b7a8d";
const tip = { background: "#1d2937", border: "1px solid #26323f", borderRadius: 8, fontSize: 12 };
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

export function MultiSeriesChart({ data, labels, height = 240 }:
  { data: SeriesMap; labels?: Record<string, string>; height?: number }) {
  const keys = Object.keys(data);
  const merged = mergeRows(keys.map((k) => ({ key: k, pts: data[k] })));
  return (
    <ResponsiveContainer width="100%" height={height}>
      <ComposedChart data={merged}>
        <CartesianGrid stroke={GRID} />
        <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={axisFmt} stroke={AXIS} fontSize={11} />
        <YAxis stroke={AXIS} fontSize={11} tickFormatter={(v) => fmt(v)} width={60} />
        <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={tip} />
        <Legend />
        {keys.map((k, i) => (
          <Line key={k} type="monotone" dataKey={k} name={labels?.[k] || k} stroke={COLORS[i % COLORS.length]} dot={false} strokeWidth={2} />
        ))}
      </ComposedChart>
    </ResponsiveContainer>
  );
}

export function OverlayChart({ reference, meters, labels, height = 260 }:
  { reference: Pt[]; meters: SeriesMap; labels?: Record<string, string>; height?: number }) {
  const keys = Object.keys(meters);
  const merged = mergeRows([{ key: "__plug", pts: reference }, ...keys.map((k) => ({ key: k, pts: meters[k] }))]);
  return (
    <ResponsiveContainer width="100%" height={height}>
      <ComposedChart data={merged}>
        <CartesianGrid stroke={GRID} />
        <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={axisFmt} stroke={AXIS} fontSize={11} />
        <YAxis yAxisId="L" stroke="#fbbf24" fontSize={11} width={55} tickFormatter={(v) => fmt(v)} />
        <YAxis yAxisId="R" orientation="right" stroke="#2dd4bf" fontSize={11} width={55} tickFormatter={(v) => fmt(v)} />
        <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={tip} />
        <Legend />
        <Area yAxisId="L" dataKey="__plug" name="monitored power (W)" stroke="#fbbf24" fill="#fbbf24" fillOpacity={0.14} dot={false} />
        {keys.map((k, i) => (
          <Line yAxisId="R" key={k} type="monotone" dataKey={k} name={labels?.[k] || `meter ${k}`} stroke={COLORS[(i + 1) % COLORS.length]} dot={false} strokeWidth={2} />
        ))}
      </ComposedChart>
    </ResponsiveContainer>
  );
}

export function CorrelationBar({ r }: { r: number | null }) {
  if (r === null || r === undefined) return <span className="text-faint">–</span>;
  const pct = Math.max(0, Math.min(1, r)) * 100;
  const color = r >= 0.8 ? "#4ade80" : r >= 0.5 ? "#fbbf24" : "#6b7a8d";
  return (
    <div className="relative h-5 w-full min-w-[90px] overflow-hidden rounded bg-surface2" title={`r=${r}`}>
      <div className="h-full rounded" style={{ width: `${pct}%`, background: color, opacity: 0.85 }} />
      <span className="absolute inset-0 grid place-items-center text-xs tabular-nums text-text/90">{r.toFixed(2)}</span>
    </div>
  );
}

const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

// Heatmap renders an hour-of-day × day-of-week consumption grid.
export function Heatmap({ cells }: { cells: HeatCell[] }) {
  const max = Math.max(0.0001, ...cells.map((c) => c.value));
  const grid: Record<string, number> = {};
  cells.forEach((c) => { grid[`${c.dow}-${c.hour}`] = c.value; });
  return (
    <div className="overflow-x-auto">
      <div className="inline-grid gap-[2px]" style={{ gridTemplateColumns: `auto repeat(24, 14px)` }}>
        <div />
        {Array.from({ length: 24 }, (_, h) => (
          <div key={h} className="text-center text-[9px] text-faint">{h % 3 === 0 ? h : ""}</div>
        ))}
        {DOW.map((d, dow) => (
          <Fragment key={dow}>
            <div className="pr-2 text-right text-[10px] leading-[14px] text-muted">{d}</div>
            {Array.from({ length: 24 }, (_, h) => {
              const v = grid[`${dow}-${h}`] || 0;
              const a = v / max;
              return (
                <div key={`${dow}-${h}`} title={`${d} ${h}:00 — ${fmt(v, 2)}`}
                  className="h-[14px] w-[14px] rounded-[2px]"
                  style={{ background: v ? `rgba(45,212,191,${0.12 + a * 0.8})` : "#161f29" }} />
              );
            })}
          </Fragment>
        ))}
      </div>
    </div>
  );
}
