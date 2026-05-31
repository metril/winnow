import {
  Area, ComposedChart, Line, CartesianGrid, ResponsiveContainer,
  Tooltip, XAxis, YAxis, Legend,
} from "recharts";
import { fmt, shortTs, tsMs } from "../util";

const COLORS = ["#4db6ac", "#ffca28", "#7e57c2", "#ef5350", "#42a5f5", "#66bb6a", "#ec407a", "#ffa726"];

interface Pt { bucket: string; value: number }
type SeriesMap = Record<string, Pt[]>;

// MultiSeriesChart overlays several meters' series on one time axis.
export function MultiSeriesChart({ data, labels, height = 240 }:
  { data: SeriesMap; labels?: Record<string, string>; height?: number }) {
  // merge into rows keyed by bucket ms
  const rows = new Map<number, any>();
  const keys = Object.keys(data);
  keys.forEach((k) => data[k].forEach((p) => {
    const t = tsMs(p.bucket);
    const row = rows.get(t) || { t };
    row[k] = p.value;
    rows.set(t, row);
  }));
  const merged = [...rows.values()].sort((a, b) => a.t - b.t);
  return (
    <ResponsiveContainer width="100%" height={height}>
      <ComposedChart data={merged}>
        <CartesianGrid stroke="#2a3340" />
        <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time"
          tickFormatter={(t) => shortTs(new Date(t).toISOString()).slice(5, 16)} stroke="#8a94a6" fontSize={11} />
        <YAxis stroke="#8a94a6" fontSize={11} tickFormatter={(v) => fmt(v)} width={60} />
        <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())}
          formatter={(v: any) => fmt(v)} contentStyle={{ background: "#171c24", border: "1px solid #2a3340" }} />
        <Legend />
        {keys.map((k, i) => (
          <Line key={k} type="monotone" dataKey={k} name={labels?.[k] || k}
            stroke={COLORS[i % COLORS.length]} dot={false} strokeWidth={2} />
        ))}
      </ComposedChart>
    </ResponsiveContainer>
  );
}

// OverlayChart shows plug power (area, left axis) vs candidate meter deltas
// (lines, right axis) — the spike lining up is the identification signal.
export function OverlayChart({ reference, meters, labels, height = 260 }:
  { reference: Pt[]; meters: SeriesMap; labels?: Record<string, string>; height?: number }) {
  const rows = new Map<number, any>();
  reference.forEach((p) => {
    const t = tsMs(p.bucket);
    const row = rows.get(t) || { t };
    row.__plug = p.value;
    rows.set(t, row);
  });
  const keys = Object.keys(meters);
  keys.forEach((k) => meters[k].forEach((p) => {
    const t = tsMs(p.bucket);
    const row = rows.get(t) || { t };
    row[k] = p.value;
    rows.set(t, row);
  }));
  const merged = [...rows.values()].sort((a, b) => a.t - b.t);
  return (
    <ResponsiveContainer width="100%" height={height}>
      <ComposedChart data={merged}>
        <CartesianGrid stroke="#2a3340" />
        <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time"
          tickFormatter={(t) => shortTs(new Date(t).toISOString()).slice(5, 16)} stroke="#8a94a6" fontSize={11} />
        <YAxis yAxisId="L" stroke="#ffca28" fontSize={11} width={55} tickFormatter={(v) => fmt(v)} />
        <YAxis yAxisId="R" orientation="right" stroke="#4db6ac" fontSize={11} width={55} tickFormatter={(v) => fmt(v)} />
        <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())}
          formatter={(v: any) => fmt(v)} contentStyle={{ background: "#171c24", border: "1px solid #2a3340" }} />
        <Legend />
        <Area yAxisId="L" dataKey="__plug" name="plug power (W)" stroke="#ffca28"
          fill="#ffca28" fillOpacity={0.15} dot={false} />
        {keys.map((k, i) => (
          <Line yAxisId="R" key={k} type="monotone" dataKey={k} name={labels?.[k] || `meter ${k}`}
            stroke={COLORS[(i + 1) % COLORS.length]} dot={false} strokeWidth={2} />
        ))}
      </ComposedChart>
    </ResponsiveContainer>
  );
}

// CorrelationBar renders a 0..1 Pearson r as a labeled bar.
export function CorrelationBar({ r }: { r: number | null }) {
  if (r === null || r === undefined) return <span className="muted">–</span>;
  const pct = Math.max(0, Math.min(1, r)) * 100;
  const color = r >= 0.8 ? "var(--green)" : r >= 0.5 ? "var(--gold)" : "var(--muted)";
  return (
    <div className="rbar" title={`r=${r}`}>
      <div className="rbar-fill" style={{ width: `${pct}%`, background: color }} />
      <span className="rbar-label">{r.toFixed(2)}</span>
    </div>
  );
}
