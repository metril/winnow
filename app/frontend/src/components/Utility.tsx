import { Bar, BarChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { Gauge, DollarSign, CalendarRange, Settings as SettingsIcon, Receipt } from "lucide-react";
import { api, UtilitySeries } from "../api";
import { useFetch } from "../fetch";
import { useLive } from "../live";
import { fmt } from "../util";
import { Page, View } from "./shell";
import { Card, CardHeader, CardBody, StatCard, Badge, Button, EmptyState, Skeleton } from "../ui";
import { useChartTheme } from "./chartTheme";

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

      <Card>
        <CardHeader title="Billed energy" icon={<Receipt size={16} />}
          subtitle={hasMeter
            ? "Your utility bill (gold) vs what winnow's published meter recorded for the same periods (teal) — they should match if winnow is tracking your meter."
            : "Your billed energy per period. Publish your meter (on the Identify/Meters page) to overlay what winnow recorded and reconcile against the bill."} />
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
                </BarChart>
              </ResponsiveContainer>}
        </CardBody>
      </Card>

      <p className="px-1 text-micro text-tertiary">
        Source statistic <span className="mono">{d.statistic_id}</span>
        {d.reconcile_meters.length > 0 && <> · reconciled against meter{d.reconcile_meters.length > 1 ? "s" : ""} {d.reconcile_meters.map((m) => `#${m}`).join(", ")}</>}
        {" "}· billed energy is ~48h delayed.
      </p>
    </Page>
  );
}
