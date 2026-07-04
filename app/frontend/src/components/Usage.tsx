import { useEffect, useMemo, useState } from "react";
import { BarChart3, CalendarDays, Flame, Gauge, Sigma, TrendingUp } from "lucide-react";
import { Bar, CartesianGrid, ComposedChart, Legend, Line, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { api, Consumption, Meter } from "../api";
import { useFetch } from "../fetch";
import { useLiveMeta } from "../live";
import { fmt } from "../util";
import { replaceHash } from "../route";
import { Page, View } from "./shell";
import { Badge, Card, CardBody, CardHeader, EmptyState, Select, Skeleton, StatCard } from "../ui";
import { useChartTheme } from "./chartTheme";
import PeriodNav, { PeriodView } from "./PeriodNav";

const isPeriodView = (v: string): v is PeriodView => v === "day" || v === "week" || v === "month" || v === "year";

function periodLabel(view: PeriodView, anchor: string): string {
  const dt = new Date(anchor + "T00:00:00");
  switch (view) {
    case "day": return dt.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
    case "week": return "Week of " + dt.toLocaleDateString(undefined, { month: "short", day: "numeric" });
    case "month": return dt.toLocaleDateString(undefined, { month: "long", year: "numeric" });
    default: return String(dt.getFullYear());
  }
}

// ConsumptionBrowser is the reusable period pager: PeriodNav + summary tiles +
// the kWh chart for ONE meter. It owns view/anchor state; the server owns the
// calendar (bucket boundaries, DST, prev/next cursors). Also embedded as the
// Usage tab of MeterDetail.
export function ConsumptionBrowser({ id, initialView = "week", initialAnchor = "", onPeriod }: {
  id: number; initialView?: PeriodView; initialAnchor?: string;
  onPeriod?: (view: PeriodView, anchor: string) => void;
}) {
  const ch = useChartTheme();
  const [view, setView] = useState<PeriodView>(initialView);
  const [anchor, setAnchor] = useState(initialAnchor); // "" = the current period
  const q = useFetch(() => api.consumption(id, view, anchor || undefined, "monitored,utility"), [id, view, anchor]);
  const d = q.data;

  useEffect(() => { if (d) onPeriod?.(view, d.anchor); /* eslint-disable-next-line */ }, [d?.anchor, view]);

  // ←/→ page periods (ignored while typing in a form control)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (t && /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return;
      if (e.key === "ArrowLeft" && d?.prev_anchor) setAnchor(d.prev_anchor);
      if (e.key === "ArrowRight" && d?.next_anchor) setAnchor(d.next_anchor);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [d?.prev_anchor, d?.next_anchor]);

  const rows = useMemo(() => (d?.buckets ?? []).map((b) => ({
    label: b.label, value: b.value, monitored: b.monitored ?? null, utility: b.utility_est ?? null,
  })), [d]);
  const hasMon = rows.some((r) => r.monitored != null);
  const hasUtl = rows.some((r) => r.utility != null);

  const peak = useMemo(() => {
    let best: { label: string; value: number } | null = null;
    for (const r of rows) if (r.value != null && (!best || r.value > best.value)) best = { label: r.label, value: r.value };
    return best;
  }, [rows]);

  const vsPrev = useMemo(() => {
    if (!d || d.total == null || d.prev_total == null || d.prev_total === 0) return undefined;
    const pct = ((d.total - d.prev_total) / d.prev_total) * 100;
    return { dir: pct > 1 ? "up" as const : pct < -1 ? "down" as const : "flat" as const, text: `${pct > 0 ? "+" : ""}${fmt(pct, 0)}% vs prev` };
  }, [d]);

  if (q.error) {
    return <EmptyState icon={<BarChart3 size={20} />} title="Couldn't load usage">{q.error}</EmptyState>;
  }
  if (!d) return <Skeleton className="h-80" />;

  const unit = d.unit;
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <PeriodNav view={view} label={periodLabel(view, d.anchor)}
          prevDisabled={!d.prev_anchor} nextDisabled={!d.next_anchor}
          onView={setView}
          onPrev={() => d.prev_anchor && setAnchor(d.prev_anchor)}
          onNext={() => d.next_anchor && setAnchor(d.next_anchor)}
          onToday={() => setAnchor("")} />
        {!d.calibrated && (
          <Badge tone="default" className="ml-auto">raw counter units — calibrate on Identify to show kWh</Badge>
        )}
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Total" value={d.total != null ? fmt(d.total, 1) : "—"} unit={unit} icon={<Sigma size={15} />} tone="brand" delta={vsPrev} />
        <StatCard label="Avg / day" value={d.avg_per_day != null ? fmt(d.avg_per_day, 1) : "—"} unit={`${unit}/day`} icon={<CalendarDays size={15} />} />
        <StatCard label={`Peak ${d.granularity}`} value={peak ? fmt(peak.value, 1) : "—"} unit={peak ? unit : undefined} icon={<Flame size={15} />}
          delta={peak ? { dir: "flat", text: peak.label } : undefined} />
        <StatCard label="Coverage" value={fmt(d.coverage * 100, 0)} unit="%" icon={<Gauge size={15} />}
          delta={d.coverage < 0.9 ? { dir: "down", text: "gaps in capture" } : undefined} />
      </div>

      {d.coverage === 0 ? (
        <EmptyState icon={<BarChart3 size={20} />} title="No data in this period">
          Nothing was heard from #{id} here — page with ‹ › or jump back to Today.
        </EmptyState>
      ) : (
        <ResponsiveContainer width="100%" height={300}>
          <ComposedChart data={rows}>
            <CartesianGrid {...ch.gridProps} />
            <XAxis dataKey="label" {...ch.axisX} minTickGap={16} />
            <YAxis tickFormatter={(v) => fmt(v)} {...ch.axisY} />
            <Tooltip contentStyle={ch.tooltipStyle} formatter={(v: any, n: any) => [`${fmt(v, 2)} ${unit}`, n]} />
            {(hasMon || hasUtl) && <Legend />}
            <Bar name={`metered (${unit})`} dataKey="value" fill={ch.brand} radius={[3, 3, 0, 0]} isAnimationActive={false} />
            {hasMon && <Line name="monitored (kWh)" dataKey="monitored" stroke={ch.gold} strokeWidth={1.5}
              dot={{ r: 2 }} activeDot={{ r: 4 }} isAnimationActive={false} />}
            {hasUtl && <Line name="bill est. (kWh)" dataKey="utility" stroke={ch.axis} strokeDasharray="4 3" strokeWidth={1.5}
              dot={false} activeDot={{ r: 3 }} isAnimationActive={false} />}
          </ComposedChart>
        </ResponsiveContainer>
      )}
    </div>
  );
}

// pickDefault: the meter you almost certainly came here for — yours, else the
// labeled candidate, else a published one, else the busiest.
function pickDefault(ms: Meter[]): number | null {
  const mine = ms.find((m) => m.is_mine);
  if (mine) return mine.endpoint_id;
  const cand = ms.find((m) => m.is_candidate && m.label);
  if (cand) return cand.endpoint_id;
  const pub = ms.find((m) => m.publish);
  if (pub) return pub.endpoint_id;
  return ms.length ? ms[0].endpoint_id : null;
}

export default function Usage({ params, onNav }: { params: string[]; onNav: (v: View, p?: (string | number)[]) => void }) {
  const { configVersion } = useLiveMeta();
  const meters = useFetch(() => api.meters("?include_ignored=false"), [configVersion]);
  const [sel, setSel] = useState<number | null>(params[0] ? Number(params[0]) : null);
  const initialView = params[1] && isPeriodView(params[1]) ? params[1] : "week";
  const initialAnchor = params[2] ?? "";

  useEffect(() => {
    if (sel == null && meters.data?.length) setSel(pickDefault(meters.data));
    // eslint-disable-next-line
  }, [meters.data]);

  const options = useMemo(() => {
    const ms = meters.data ?? [];
    const score = (m: Meter) => (m.is_mine ? 4 : 0) + (m.is_candidate ? 2 : 0) + (m.publish ? 2 : 0) + (m.label ? 1 : 0);
    const sorted = [...ms].sort((a, b) => score(b) - score(a) || (b.packets ?? 0) - (a.packets ?? 0)).slice(0, 60);
    if (sel != null && !sorted.some((m) => m.endpoint_id === sel)) {
      sorted.unshift({ endpoint_id: sel } as Meter);
    }
    return sorted;
  }, [meters.data, sel]);

  return (
    <Page title="Usage" breadcrumb="How much energy your meter records — by day, week, month or year"
      actions={
        <Select value={sel ?? ""} onChange={(e) => setSel(Number(e.target.value))}>
          {sel == null && <option value="">select a meter…</option>}
          {options.map((m) => (
            <option key={m.endpoint_id} value={m.endpoint_id}>
              #{m.endpoint_id}{m.label ? ` — ${m.label}` : ""}{m.is_mine ? " ★" : ""}
            </option>
          ))}
        </Select>
      }>
      {sel == null ? (
        meters.loading ? <Skeleton className="h-80" /> : (
          <EmptyState icon={<TrendingUp size={20} />} title="No meter picked yet">
            Identify your meter first, then this page becomes its usage browser.
          </EmptyState>
        )
      ) : (
        <Card>
          <CardHeader title={`Meter #${sel}`} icon={<BarChart3 size={16} />}
            subtitle="Bars are what this meter recorded; the gold line is your monitored (HA) consumption; the dashed line is the utility bill's daily estimate."
            actions={<a className="text-small text-brand hover:underline" href={`#/meters/${sel}`}>Open in Meters →</a>} />
          <CardBody>
            <ConsumptionBrowser key={sel} id={sel} initialView={initialView} initialAnchor={initialAnchor}
              onPeriod={(v, a) => replaceHash("usage", [sel, v, a])} />
          </CardBody>
        </Card>
      )}
    </Page>
  );
}
