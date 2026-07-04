import { useEffect, useMemo, useState } from "react";
import { BarChart3, CalendarDays, ChevronLeft, ChevronRight, Flame, Gauge, Sigma, TrendingUp, X } from "lucide-react";
import { Bar, CartesianGrid, ComposedChart, Legend, Line, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { api, Consumption, Meter } from "../api";
import { useFetch } from "../fetch";
import { useLiveMeta } from "../live";
import { fmt } from "../util";
import { replaceHash } from "../route";
import { Page, View } from "./shell";
import { Badge, Button, Card, CardBody, CardHeader, EmptyState, FetchError, IconButton, Select, Skeleton, StatCard } from "../ui";
import { useChartTheme } from "./chartTheme";
import PeriodNav, { PeriodView } from "./PeriodNav";

const MAX_COMPARE = 4;

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

function vsPrevDelta(d: Consumption) {
  if (d.total == null || d.prev_total == null || d.prev_total === 0) return undefined;
  const pct = ((d.total - d.prev_total) / d.prev_total) * 100;
  return { dir: pct > 1 ? "up" as const : pct < -1 ? "down" as const : "flat" as const, text: `${pct > 0 ? "+" : ""}${fmt(pct, 0)}% vs prev` };
}

function peakBucket(d: Consumption) {
  let best: { label: string; value: number } | null = null;
  for (const b of d.buckets) if (b.value != null && (!best || b.value > best.value)) best = { label: b.label, value: b.value };
  return best;
}

// ConsumptionChart renders one meter's period page: usage bars + monitored /
// bill-estimate overlay lines. yMax (optional) pins the Y domain so side-by-side
// panels compare magnitudes honestly.
function ConsumptionChart({ d, yMax, height = 300 }: { d: Consumption; yMax?: number; height?: number }) {
  const ch = useChartTheme();
  const rows = useMemo(() => d.buckets.map((b) => ({
    label: b.label, value: b.value, monitored: b.monitored ?? null, utility: b.utility_est ?? null,
  })), [d]);
  const hasMon = rows.some((r) => r.monitored != null);
  const hasUtl = rows.some((r) => r.utility != null);
  if (d.coverage === 0) {
    return (
      <EmptyState icon={<BarChart3 size={20} />} title="No data in this period">
        Nothing was heard from #{d.endpoint_id} here — page with ‹ › or jump back to Today.
      </EmptyState>
    );
  }
  return (
    <ResponsiveContainer width="100%" height={height}>
      <ComposedChart data={rows}>
        <CartesianGrid {...ch.gridProps} />
        <XAxis dataKey="label" {...ch.axisX} minTickGap={16} />
        <YAxis tickFormatter={(v) => fmt(v)} domain={yMax ? [0, yMax] : undefined} {...ch.axisY} />
        <Tooltip contentStyle={ch.tooltipStyle} formatter={(v: any, n: any) => [`${fmt(v, 2)} ${d.unit}`, n]} />
        {(hasMon || hasUtl) && <Legend />}
        <Bar name={`metered (${d.unit})`} dataKey="value" fill={ch.brand} radius={[3, 3, 0, 0]} isAnimationActive={false} />
        {hasMon && <Line name="monitored (kWh)" dataKey="monitored" stroke={ch.gold} strokeWidth={1.5}
          dot={{ r: 2 }} activeDot={{ r: 4 }} isAnimationActive={false} />}
        {/* target-line idiom: full-contrast ink, dashed — ch.axis (tick gray)
            was invisible against the card background */}
        {hasUtl && <Line name="bill est. (kWh)" dataKey="utility" stroke={ch.text} strokeOpacity={0.75}
          strokeDasharray="6 4" strokeWidth={1.5}
          dot={false} activeDot={{ r: 3 }} isAnimationActive={false} />}
      </ComposedChart>
    </ResponsiveContainer>
  );
}

function ConsumptionTiles({ d }: { d: Consumption }) {
  const peak = peakBucket(d);
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <StatCard label="Total" value={d.total != null ? fmt(d.total, 1) : "—"} unit={d.unit} icon={<Sigma size={15} />} tone="brand" delta={vsPrevDelta(d)} />
      <StatCard label="Avg / day" value={d.avg_per_day != null ? fmt(d.avg_per_day, 1) : "—"} unit={`${d.unit}/day`} icon={<CalendarDays size={15} />} />
      <StatCard label={`Peak ${d.granularity}`} value={peak ? fmt(peak.value, 1) : "—"} unit={peak ? d.unit : undefined} icon={<Flame size={15} />}
        delta={peak ? { dir: "flat", text: peak.label } : undefined} />
      <StatCard label="Coverage" value={fmt(d.coverage * 100, 0)} unit="%" icon={<Gauge size={15} />}
        delta={d.coverage < 0.9 ? { dir: "down", text: "gaps in capture" } : undefined} />
    </div>
  );
}

// CompactStats is the one-line summary on comparison panels.
function CompactStats({ d }: { d: Consumption }) {
  const delta = vsPrevDelta(d);
  return (
    <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1 text-small">
      <span className="tabular-nums text-text">{d.total != null ? `${fmt(d.total, 1)} ${d.unit}` : "no data"}</span>
      {d.avg_per_day != null && <span className="tabular-nums text-tertiary">{fmt(d.avg_per_day, 1)} {d.unit}/day</span>}
      {delta && <span className={"tabular-nums " + (delta.dir === "up" ? "text-good" : delta.dir === "down" ? "text-bad" : "text-tertiary")}>{delta.text}</span>}
    </div>
  );
}

// ←/→ page periods (ignored while typing in a form control)
function useArrowPaging(d: Consumption | null, setAnchor: (a: string) => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (t && /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return;
      if (e.key === "ArrowLeft" && d?.prev_anchor) setAnchor(d.prev_anchor);
      if (e.key === "ArrowRight" && d?.next_anchor) setAnchor(d.next_anchor);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line
  }, [d?.prev_anchor, d?.next_anchor]);
}

// ConsumptionBrowser: the self-contained single-meter pager (nav + tiles +
// chart) — embedded as the Usage tab of the meter detail. The Usage page
// composes the same pieces itself so ONE pager can drive several panels.
export function ConsumptionBrowser({ id, initialView = "week", initialAnchor = "", onPeriod }: {
  id: number; initialView?: PeriodView; initialAnchor?: string;
  onPeriod?: (view: PeriodView, anchor: string) => void;
}) {
  const [view, setView] = useState<PeriodView>(initialView);
  const [anchor, setAnchor] = useState(initialAnchor); // "" = the current period
  const q = useFetch(() => api.consumption(id, view, anchor || undefined, "monitored,utility"), [id, view, anchor]);
  const d = q.data;

  useEffect(() => { if (d) onPeriod?.(view, d.anchor); /* eslint-disable-next-line */ }, [d?.anchor, view]);
  useArrowPaging(d, setAnchor);

  if (q.error) return <FetchError error={q.error} onRetry={q.reload} />;
  if (!d) return <Skeleton className="h-80" />;
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <PeriodNav view={view} label={periodLabel(view, d.anchor)}
          prevDisabled={!d.prev_anchor} nextDisabled={!d.next_anchor}
          onView={setView}
          onPrev={() => d.prev_anchor && setAnchor(d.prev_anchor)}
          onNext={() => d.next_anchor && setAnchor(d.next_anchor)}
          onToday={() => setAnchor("")} />
        {!d.calibrated && <Badge tone="default" className="ml-auto">raw counter units — calibrate on Identify to show kWh</Badge>}
      </div>
      <ConsumptionTiles d={d} />
      <ConsumptionChart d={d} />
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

  // hash: #/usage/<primary>[+id2+id3]/<view>/<anchor>
  const initIds = (params[0] || "").split("+").map(Number).filter((n) => Number.isFinite(n) && n > 0);
  const [primary, setPrimary] = useState<number | null>(initIds[0] ?? null);
  const [compare, setCompare] = useState<number[]>(initIds.slice(1, 1 + MAX_COMPARE));
  const [view, setView] = useState<PeriodView>(params[1] && isPeriodView(params[1]) ? params[1] : "week");
  const [anchor, setAnchor] = useState(params[2] ?? "");

  useEffect(() => {
    if (primary == null && meters.data?.length) setPrimary(pickDefault(meters.data));
    // eslint-disable-next-line
  }, [meters.data]);

  // ONE aligned fetch for every panel: primary first, then comparisons.
  const compareKey = compare.join("+");
  const q = useFetch<Consumption[] | null>(() => {
    if (primary == null) return Promise.resolve(null);
    return Promise.all([primary, ...compare].map((id) => api.consumption(id, view, anchor || undefined, "monitored,utility")));
  }, [primary, compareKey, view, anchor, configVersion]);
  const results = q.data;
  const main = results?.[0] ?? null;

  useEffect(() => {
    if (primary != null && main) replaceHash("usage", [[primary, ...compare].join("+"), view, main.anchor]);
    // eslint-disable-next-line
  }, [primary, compareKey, view, main?.anchor]);
  useArrowPaging(main, setAnchor);

  // shared Y scale across all panels so magnitudes compare honestly
  const yMax = useMemo(() => {
    let max = 0;
    for (const d of results ?? []) {
      for (const b of d.buckets) {
        for (const v of [b.value, b.monitored, b.utility_est]) if (v != null && v > max) max = v;
      }
    }
    return max > 0 ? Math.ceil(max * 1.05) : undefined;
  }, [results]);

  const options = useMemo(() => {
    const ms = meters.data ?? [];
    const score = (m: Meter) => (m.is_mine ? 4 : 0) + (m.is_candidate ? 2 : 0) + (m.publish ? 2 : 0) + (m.label ? 1 : 0);
    const sorted = [...ms].sort((a, b) => score(b) - score(a) || (b.packets ?? 0) - (a.packets ?? 0)).slice(0, 60);
    if (primary != null && !sorted.some((m) => m.endpoint_id === primary)) sorted.unshift({ endpoint_id: primary } as Meter);
    return sorted;
  }, [meters.data, primary]);

  // ‹ › cycle the PRIMARY meter through the option list
  const cycleMeter = (dir: 1 | -1) => {
    if (primary == null || !options.length) return;
    const i = options.findIndex((m) => m.endpoint_id === primary);
    const next = options[(i + dir + options.length) % options.length];
    setPrimary(next.endpoint_id);
  };

  const addCompare = (id: number) => {
    if (!id || id === primary || compare.includes(id) || compare.length >= MAX_COMPARE) return;
    setCompare((c) => [...c, id]);
  };
  const removeCompare = (id: number) => setCompare((c) => c.filter((x) => x !== id));

  const meterName = (id: number) => {
    const m = (meters.data ?? []).find((x) => x.endpoint_id === id);
    return m?.label ? `#${id} — ${m.label}` : `#${id}`;
  };

  return (
    <Page title="Usage" breadcrumb="How much energy your meters record — by day, week, month or year"
      actions={
        <div className="flex items-center gap-1">
          <Button size="sm" variant="ghost" aria-label="Previous meter" icon={<ChevronLeft size={14} />} onClick={() => cycleMeter(-1)} disabled={primary == null} />
          <Select value={primary ?? ""} onChange={(e) => setPrimary(Number(e.target.value))}>
            {primary == null && <option value="">select a meter…</option>}
            {options.map((m) => (
              <option key={m.endpoint_id} value={m.endpoint_id}>
                #{m.endpoint_id}{m.label ? ` — ${m.label}` : ""}{m.is_mine ? " ★" : ""}
              </option>
            ))}
          </Select>
          <Button size="sm" variant="ghost" aria-label="Next meter" icon={<ChevronRight size={14} />} onClick={() => cycleMeter(1)} disabled={primary == null} />
        </div>
      }>
      {primary == null ? (
        meters.loading ? <Skeleton className="h-80" /> : (
          <EmptyState icon={<TrendingUp size={20} />} title="No meter picked yet">
            Identify your meter first, then this page becomes its usage browser.
          </EmptyState>
        )
      ) : q.error ? <FetchError error={q.error} onRetry={q.reload} /> : (
        <>
          <div className="flex flex-wrap items-center gap-3">
            {main
              ? <PeriodNav view={view} label={periodLabel(view, main.anchor)}
                  prevDisabled={!main.prev_anchor} nextDisabled={!main.next_anchor}
                  onView={setView}
                  onPrev={() => main.prev_anchor && setAnchor(main.prev_anchor)}
                  onNext={() => main.next_anchor && setAnchor(main.next_anchor)}
                  onToday={() => setAnchor("")} />
              : <Skeleton className="h-8 w-96" />}
            <div className="ml-auto flex flex-wrap items-center gap-1.5">
              {compare.map((id) => (
                <Badge key={id} tone="brand">
                  {meterName(id)}
                  <button aria-label={`Remove ${id}`} className="ml-0.5 hover:text-bad" onClick={() => removeCompare(id)}><X size={11} /></button>
                </Badge>
              ))}
              {compare.length < MAX_COMPARE && (
                <Select value="" aria-label="Add comparison" onChange={(e) => { addCompare(Number(e.target.value)); e.target.value = ""; }}>
                  <option value="">+ compare…</option>
                  {options.filter((m) => m.endpoint_id !== primary && !compare.includes(m.endpoint_id)).map((m) => (
                    <option key={m.endpoint_id} value={m.endpoint_id}>#{m.endpoint_id}{m.label ? ` — ${m.label}` : ""}</option>
                  ))}
                </Select>
              )}
            </div>
          </div>

          {!results ? <Skeleton className="h-96" /> : (
            <>
              <Card>
                <CardHeader title={meterName(primary)} icon={<BarChart3 size={16} />}
                  subtitle="Bars are what this meter recorded; the gold line is your monitored (HA) consumption; the dashed line is the bill's daily estimate — projected from past bills for the same month until the real bill posts."
                  actions={<>
                    {main && !main.calibrated && <Badge tone="default">raw counter units</Badge>}
                    <a className="text-small text-brand hover:underline" href={`#/meters/${primary}`}>Open in Meters →</a>
                  </>} />
                <CardBody className="space-y-4">
                  {main && <ConsumptionTiles d={main} />}
                  {main && <ConsumptionChart d={main} yMax={yMax} />}
                </CardBody>
              </Card>

              {compare.map((id, i) => {
                const d = results[i + 1];
                if (!d) return null;
                return (
                  <Card key={id}>
                    <CardHeader title={meterName(id)} icon={<BarChart3 size={16} />}
                      actions={<>
                        <CompactStats d={d} />
                        <IconButton label={`Remove ${id}`} onClick={() => removeCompare(id)}><X size={14} /></IconButton>
                      </>} />
                    <CardBody>
                      <ConsumptionChart d={d} yMax={yMax} height={200} />
                    </CardBody>
                  </Card>
                );
              })}
            </>
          )}
        </>
      )}
    </Page>
  );
}
