import {
  Activity, Radio, Gauge, DollarSign, AlertTriangle, ArrowUpRight, Crosshair, Zap,
} from "lucide-react";
import { api, PublishedLive, Anomaly } from "../api";
import { useLive, perMin } from "../live";
import { useFetch } from "../fetch";
import { useSourceLabels } from "../sources";
import { fmt } from "../util";
import { Page, View } from "./shell";
import { Card, CardHeader, CardBody, StatCard, Badge, Dot, Button, EmptyState, Skeleton } from "../ui";
import { Sparkline } from "./charts";
import { useChartTheme } from "./chartTheme";

export default function Overview({ onNav }: { onNav: (v: View) => void }) {
  const chart = useChartTheme();
  const { power, powerHistory, readings, configVersion } = useLive();
  const ov = useFetch(api.overview, [configVersion]);
  const health = useFetch(api.health, [configVersion]);
  const srcLabel = useSourceLabels();

  const pubs = ov.data?.published || [];
  const anomalies = ov.data?.anomalies || [];
  const cur = ov.data?.currency || "$";
  const costToday = pubs.reduce((s, p) => s + (p.cost_today || 0), 0);
  const todayKwh = pubs.filter((p) => p.commodity === "electric").reduce((s, p) => s + (p.today || 0), 0);
  const spark = powerHistory.map((p) => p.v);
  const rate = perMin(readings);

  return (
    <Page title="Overview" actions={<Button variant="ghost" icon={<Crosshair size={15} />} onClick={() => onNav("identify")}>Identify</Button>}>
      {/* Row 1 — hero + system status */}
      <div className="grid gap-6 lg:grid-cols-3">
        <div className="lg:col-span-2">
          {ov.data == null ? <Skeleton className="h-48" />
            : pubs.length === 0 ? <OnboardingHero onNav={onNav} />
              : <Hero pubs={pubs} power={power} spark={spark} cur={cur} />}
        </div>
        <Card>
          <CardHeader title="System" icon={<Activity size={16} />} />
          <CardBody className="space-y-1">
            <StatusRow tone={rate > 0 || health.data?.alive ? "good" : "bad"} label="Capture" value={`${rate}/min`} />
            <StatusRow tone={health.data?.alive ? "good" : "off"} label="Receiving" value={health.data?.alive ? "live" : "idle"} />
            <StatusRow tone="off" label="Meters seen" value={fmt(health.data?.unique_meters ?? 0)} />
            <StatusRow tone="off" label="Sources" value={fmt(health.data?.sources?.length ?? 0)} />
          </CardBody>
        </Card>
      </div>

      {/* Row 2 — KPIs */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard label="Monitored now" tone="gold" icon={<Activity size={15} />} value={power != null ? fmt(power) : "–"} unit="W"
          spark={spark.length > 1 ? <Sparkline data={spark} color={chart.gold} height={32} /> : undefined} />
        <StatCard label="Published" tone="brand" icon={<Radio size={15} />} value={pubs.length} unit="meters" />
        <StatCard label="Today" icon={<Gauge size={15} />} value={fmt(todayKwh, 1)} unit="kWh" />
        <StatCard label="Cost today" tone="good" icon={<DollarSign size={15} />}
          value={ov.data?.cost_per_kwh ? `${cur}${fmt(costToday, 2)}` : "–"} unit={ov.data?.cost_per_kwh ? "" : "set tariff"} />
      </div>

      {/* Row 3 — alerts */}
      {anomalies.length > 0 && (
        <Card variant="alert">
          <CardHeader title="Alerts" icon={<AlertTriangle size={16} className="text-bad" />} subtitle={`${anomalies.length} need attention`} />
          <CardBody className="space-y-2">{anomalies.map((a, i) => <AlertRow key={i} a={a} srcLabel={srcLabel} />)}</CardBody>
        </Card>
      )}

      {/* Row 4 — meters + sources */}
      <div className="grid gap-6 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader title="Your meters" subtitle="Published to Home Assistant"
            actions={<Button size="sm" variant="ghost" icon={<ArrowUpRight size={14} />} onClick={() => onNav("meters")}>All meters</Button>} />
          <CardBody>
            {pubs.length === 0
              ? <EmptyState icon={<Radio size={22} />} title="Nothing published yet"
                action={<Button variant="primary" onClick={() => onNav("identify")}>Identify your meter</Button>}>
                Find which meter is yours, then publish it.</EmptyState>
              : <div className="grid gap-3 sm:grid-cols-2">{pubs.map((p) => <PubCard key={p.endpoint_id} p={p} cur={cur} onClick={() => onNav("meters")} />)}</div>}
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Capture by source" icon={<Radio size={16} />}
            actions={<Button size="sm" variant="ghost" onClick={() => onNav("devices")}>Devices</Button>} />
          <CardBody className="space-y-2">
            {(health.data?.sources || []).length === 0 ? <EmptyState>No dongles reporting.</EmptyState>
              : health.data!.sources.map((s) => (
                <div key={s.source} className="flex items-center gap-2 text-small">
                  <Dot tone={s.alive ? "good" : "bad"} />
                  <span className="truncate text-secondary">{srcLabel(s.source)}</span>
                  <span className="ml-auto tabular-nums text-tertiary">{perMin(readings, s.source) || s.packets_last_min}/min</span>
                </div>
              ))}
          </CardBody>
        </Card>
      </div>
    </Page>
  );
}

function Hero({ pubs, power, spark, cur }: { pubs: PublishedLive[]; power: number | null; spark: number[]; cur: string }) {
  const p = pubs[0];
  return (
    <Card variant="accent" className="relative overflow-hidden">
      <div className="pointer-events-none absolute inset-0" style={{ background: "radial-gradient(600px 240px at 85% -20%, rgba(45,212,191,0.10), transparent 60%)" }} />
      <CardBody>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <span className="text-h3">{p.name || `Meter #${p.endpoint_id}`}</span>
            <Badge tone="gold"><Radio size={11} /> feeding HA</Badge>
          </div>
          <span className="id-pill">#{p.endpoint_id}</span>
        </div>
        <div className="mt-4 flex items-end gap-2">
          <span className="text-display tabular-nums text-brand">{power != null ? fmt(power) : "–"}</span>
          <span className="mb-1.5 text-secondary">W <span className="text-tertiary">monitored now</span></span>
        </div>
        {spark.length > 1 && <div className="mt-1 h-12"><Sparkline data={spark} height={48} /></div>}
        <div className="mt-4 flex flex-wrap gap-x-8 gap-y-2 border-t border-border pt-3 text-small">
          <Metric label="Today" value={`${fmt(p.today, 2)} ${p.unit}`} />
          {p.cost_today > 0 && <Metric label="Cost today" value={`${cur}${fmt(p.cost_today, 2)}`} tone="text-good" />}
          {p.rate != null && <Metric label="Rate" value={`${fmt(p.rate, 2)} ${p.unit}/h`} />}
          {pubs.length > 1 && <Metric label="Also publishing" value={`${pubs.length - 1} more`} />}
        </div>
      </CardBody>
    </Card>
  );
}

function OnboardingHero({ onNav }: { onNav: (v: View) => void }) {
  return (
    <Card className="relative overflow-hidden">
      <div className="pointer-events-none absolute inset-0" style={{ background: "radial-gradient(600px 240px at 85% -20%, rgba(45,212,191,0.08), transparent 60%)" }} />
      <CardBody className="py-8">
        <div className="flex items-center gap-2 text-brand"><Crosshair size={18} /><span className="text-h3 text-text">Find your meter</span></div>
        <p className="mt-2 max-w-lg text-small text-secondary">
          winnow is listening for your building's meter broadcasts. Pick your Home-Assistant power
          sensors as ground truth, then let winnow identify which meter is yours and feed it back to HA.
        </p>
        <div className="mt-4 flex gap-2">
          <Button variant="primary" icon={<Crosshair size={15} />} onClick={() => onNav("identify")}>Start identifying</Button>
          <Button variant="default" icon={<Zap size={15} />} onClick={() => onNav("loadtests")}>Run a load test</Button>
        </div>
      </CardBody>
    </Card>
  );
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return <div><div className="label">{label}</div><div className={"mt-0.5 tabular-nums " + (tone || "text-text")}>{value}</div></div>;
}
function StatusRow({ tone, label, value }: { tone: "good" | "bad" | "warn" | "off"; label: string; value: React.ReactNode }) {
  return <div className="flex items-center gap-2 text-small"><Dot tone={tone} /><span className="text-secondary">{label}</span><span className="ml-auto tabular-nums text-tertiary">{value}</span></div>;
}
function PubCard({ p, cur, onClick }: { p: PublishedLive; cur: string; onClick: () => void }) {
  return (
    <Card variant="interactive" onClick={onClick} className="p-4">
      <div className="flex items-center justify-between">
        <span className="truncate font-medium">{p.name || `meter ${p.endpoint_id}`}</span>
        <Badge tone={p.commodity === "electric" ? "gold" : "brand"}>{p.commodity}</Badge>
      </div>
      <div className="mt-2 flex items-end gap-1">
        <span className="text-h2 tabular-nums text-brand">{p.rate != null ? fmt(p.rate, 2) : "–"}</span>
        <span className="mb-0.5 text-micro text-tertiary">{p.unit}/h</span>
      </div>
      <div className="mt-1 flex justify-between text-micro text-tertiary">
        <span>today {fmt(p.today, 2)} {p.unit}</span>
        {p.cost_today > 0 && <span className="text-good">{cur}{fmt(p.cost_today, 2)}</span>}
      </div>
    </Card>
  );
}
function AlertRow({ a, srcLabel }: { a: Anomaly; srcLabel: (s: string) => string }) {
  const label = a.kind === "dropout" ? "Dropout" : a.kind === "stuck" ? "Stuck odometer" : "Source down";
  const who = a.source ? srcLabel(a.source) : a.endpoint_id ? `#${a.endpoint_id}` : "";
  return (
    <div className="flex items-center gap-2.5 rounded-lg bg-bad/5 px-3 py-2 text-small">
      <AlertTriangle size={15} className="shrink-0 text-bad" />
      <span className="font-medium text-text">{label}</span>
      <span className="truncate text-tertiary">{who}</span>
      <span className="ml-auto truncate text-tertiary">{a.detail.replace(/[TZ]/g, " ").trim()}</span>
    </div>
  );
}
