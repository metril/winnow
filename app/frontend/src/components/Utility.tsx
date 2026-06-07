import { Bar, BarChart, Brush, CartesianGrid, Legend, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { Gauge, DollarSign, CalendarRange, Settings as SettingsIcon, Receipt, CalendarDays } from "lucide-react";
import { api, UtilitySeries } from "../api";
import { useFetch } from "../fetch";
import { useLive } from "../live";
import { fmt } from "../util";
import { Page, View } from "./shell";
import { Card, CardHeader, CardBody, StatCard, Badge, Button, EmptyState, Skeleton, InfoHint } from "../ui";
import { useChartTheme } from "./chartTheme";

// granularityNote explains what the auto-resolved period means.
function granularityNote(period: string): string {
  if (period === "month") return "Your utility reports monthly (e.g. Eversource via Opower) — the finest granularity it exposes. winnow spreads each bill across its days as an estimate below; daily/hourly utilities give finer buckets and enable true per-bucket correlation.";
  if (period === "day") return "Your utility exposes daily buckets — winnow uses these directly, with no estimation needed.";
  if (period === "hour") return "Your utility exposes hourly buckets — the finest granularity, enabling true per-bucket correlation against your meter.";
  return "winnow auto-detected the finest granularity your utility exposes.";
}

// label a bucket start by its resolved period: month → "Mar 2026", day/hour → date.
function periodLabel(ts: string, period: string): string {
  const d = new Date(ts);
  if (period === "month") return d.toLocaleDateString(undefined, { month: "short", year: "numeric" });
  if (period === "day") return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric" });
}

export default function Utility({ onNav }: { onNav: (v: View) => void }) {
  const ch = useChartTheme();
  const { configVersion } = useLive();
  const u = useFetch(api.utilitySeries, [configVersion]);

  if (!u.data) return <Page title="Utility bill"><Skeleton className="h-64" /></Page>;
  const d: UtilitySeries = u.data;

  if (!d.statistic_id) {
    return (
      <Page title="Utility bill">
        <Card><CardBody>
          <EmptyState icon={<Receipt size={22} />} title="No utility bill connected"
            action={<Button variant="primary" icon={<SettingsIcon size={15} />} onClick={() => onNav("settings")}>Open Settings</Button>}>
            Connect your utility's energy statistic (e.g. Opower/Eversource) in Settings → Utility bill to see your billed
            usage and cost here.
          </EmptyState>
        </CardBody></Card>
      </Page>
    );
  }

  const cur = d.currency || "$";
  const pts = d.points;
  const latest = pts[pts.length - 1];
  const totalCost = d.cost_per_kwh > 0 ? d.total_kwh * d.cost_per_kwh : 0;
  const hasMeter = pts.some((p) => p.meter_kwh != null);
  const data = pts.map((p) => ({
    t: periodLabel(p.ts, d.period), bill: p.kwh, meter: p.meter_kwh, cost: p.cost, cov: Math.round(p.coverage_pct * 100),
  }));
  // Browse window: default the brush to the most recent buckets so the chart opens
  // on recent data while the handle reveals the full history.
  const winSize = d.period === "hour" ? 168 : d.period === "day" ? 60 : 24;
  const billStart = Math.max(0, data.length - winSize);

  const est = d.daily_estimate || [];
  const hasShaped = est.some((e) => e.shaped_kwh != null);
  const hasDayMeter = est.some((e) => e.meter_kwh != null);
  const days = est.map((e) => ({ t: e.day.slice(5), flat: e.flat_kwh, shaped: e.shaped_kwh, meter: e.meter_kwh }));
  const dayStart = Math.max(0, days.length - 60);

  return (
    <Page title="Utility bill"
      actions={<>
        <Badge tone="brand">{d.period}</Badge>
        <Button variant="ghost" icon={<SettingsIcon size={15} />} onClick={() => onNav("settings")}>Change statistic</Button>
      </>}>

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard label={`Latest ${d.period}`} tone="gold" icon={<Gauge size={15} />}
          value={latest ? fmt(latest.kwh, 1) : "–"} unit="kWh" />
        <StatCard label={`Total (${d.bucket_count} ${d.period}s)`} icon={<CalendarRange size={15} />}
          value={fmt(d.total_kwh, 0)} unit="kWh" />
        <StatCard label="Est. cost" tone="good" icon={<DollarSign size={15} />}
          value={d.cost_per_kwh > 0 ? `${cur}${fmt(totalCost, 2)}` : "–"} unit={d.cost_per_kwh > 0 ? "" : "set tariff"} />
        <StatCard label="Latest cost" tone="good" icon={<DollarSign size={15} />}
          value={latest?.cost != null ? `${cur}${fmt(latest.cost, 2)}` : "–"} unit={latest?.cost != null ? "" : ""} />
      </div>

      <div className="flex items-center gap-2 px-1 text-small text-secondary">
        <span className="label">Granularity</span>
        <Badge tone="brand">{d.period === "month" ? "monthly" : d.period === "day" ? "daily" : d.period === "hour" ? "hourly" : d.period}</Badge>
        <span className="text-tertiary">· {d.bucket_count} bucket{d.bucket_count === 1 ? "" : "s"} of history</span>
        <InfoHint>{granularityNote(d.period)}</InfoHint>
      </div>

      <Card>
        <CardHeader title="Billed energy" icon={<Receipt size={16} />}
          subtitle={hasMeter
            ? "Your utility bill (gold) vs what winnow's published meter recorded for the same periods (teal) — they should match if winnow is tracking your meter. Drag the slider below to browse older periods."
            : "Your billed energy per period. Publish your meter (on the Identify/Meters page) to overlay what winnow recorded and reconcile against the bill. Drag the slider below to browse older periods."} />
        <CardBody>
          {pts.length === 0
            ? <EmptyState icon={<Receipt size={20} />} title="No billing data yet">
                Utility data is ~48h delayed; it appears here after the worker's next backfill.
              </EmptyState>
            : <ResponsiveContainer width="100%" height={300}>
                <BarChart data={data}>
                  <CartesianGrid {...ch.gridProps} />
                  <XAxis dataKey="t" {...ch.axisX} />
                  <YAxis tickFormatter={(v) => fmt(v)} {...ch.axisY} />
                  <Tooltip contentStyle={ch.tooltipStyle}
                    formatter={(v: any, n: any) => [fmt(v, 1) + " kWh", n]}
                    labelFormatter={(l: any) => String(l)} />
                  <Legend />
                  <Bar name="billed" dataKey="bill" fill={ch.gold} radius={[3, 3, 0, 0]} isAnimationActive={false} />
                  {hasMeter && <Bar name="winnow meter" dataKey="meter" fill={ch.brand} radius={[3, 3, 0, 0]} isAnimationActive={false} />}
                  {data.length > winSize &&
                    <Brush dataKey="t" height={22} stroke={ch.brand} fill={ch.empty} travellerWidth={8}
                      startIndex={billStart} tickFormatter={() => ""} />}
                </BarChart>
              </ResponsiveContainer>}
        </CardBody>
      </Card>

      {days.length > 0 && (
        <Card>
          <CardHeader title="Estimated daily usage" icon={<CalendarDays size={16} />}
            subtitle="Your monthly bill spread across its days: a flat level (bill ÷ days) for reconciliation, and — when you have monitored sensors — a profile-shaped curve with real day-to-day variance. It's a weak but useful signal: the meter whose daily totals track these is likely yours. Drag the slider to browse." />
          <CardBody>
            <ResponsiveContainer width="100%" height={260}>
              <LineChart data={days}>
                <CartesianGrid {...ch.gridProps} />
                <XAxis dataKey="t" {...ch.axisX} minTickGap={24} />
                <YAxis tickFormatter={(v) => fmt(v)} {...ch.axisY} />
                <Tooltip contentStyle={ch.tooltipStyle} formatter={(v: any, n: any) => [fmt(v, 2) + " kWh", n]} />
                <Legend />
                <Line name="flat est." dataKey="flat" stroke={ch.gold} strokeDasharray="4 3" strokeWidth={1.5}
                  dot={{ r: 2 }} activeDot={{ r: 4 }} isAnimationActive={false} />
                {hasShaped && <Line name="shaped est." dataKey="shaped" stroke={ch.palette[2]} strokeWidth={1.5}
                  dot={{ r: 2 }} activeDot={{ r: 4 }} isAnimationActive={false} connectNulls />}
                {hasDayMeter && <Line name="winnow meter" dataKey="meter" stroke={ch.brand} strokeWidth={2}
                  dot={{ r: 2 }} activeDot={{ r: 4 }} isAnimationActive={false} connectNulls />}
                {days.length > 60 &&
                  <Brush dataKey="t" height={22} stroke={ch.brand} fill={ch.empty} travellerWidth={8}
                    startIndex={dayStart} tickFormatter={() => ""} />}
              </LineChart>
            </ResponsiveContainer>
          </CardBody>
        </Card>
      )}

      <p className="px-1 text-micro text-tertiary">
        Source statistic <span className="mono">{d.statistic_id}</span>
        {d.reconcile_meters.length > 0 && <> · reconciled against meter{d.reconcile_meters.length > 1 ? "s" : ""} {d.reconcile_meters.map((m) => `#${m}`).join(", ")}</>}
        {" "}· billed energy is ~48h delayed.
      </p>
    </Page>
  );
}
