import {
  Activity, Radio, Gauge, DollarSign, AlertTriangle, ArrowUpRight, Crosshair, Zap, Receipt, BarChart3, Star,
} from "lucide-react";
import { api, PublishedLive, MyMeter, Anomaly, UtilitySeries } from "../api";
import { useLive, perMin } from "../live";
import { useFetch } from "../fetch";
import { useSourceLabels } from "../sources";
import { fmt } from "../util";
import { Page, View } from "./shell";
import { Card, CardHeader, CardBody, StatCard, Badge, Dot, Button, EmptyState, FetchError, Skeleton } from "../ui";
import { Sparkline } from "./charts";
import { useChartTheme } from "./chartTheme";

export default function Overview({ onNav }: { onNav: (v: View, p?: (string | number)[]) => void }) {
  const chart = useChartTheme();
  const { power, powerHistory, readings, configVersion, connectedAt } = useLive();
  const ov = useFetch(api.overview, [configVersion]);
  const health = useFetch(api.health, [configVersion]);
  const util = useFetch(api.utilitySeries, [configVersion]);
  const srcLabel = useSourceLabels();

  const pubs = ov.data?.published || [];
  const my = ov.data?.my_meter || null;
  const anomalies = ov.data?.anomalies || [];
  const cur = ov.data?.currency || "$";
  // Prefer YOUR meter's numbers for the headline tiles; fall back to the
  // published sum for setups that publish without marking a meter as theirs.
  const costToday = my && my.calibrated ? my.cost_today : pubs.reduce((s, p) => s + (p.cost_today || 0), 0);
  const todayKwh = my && my.calibrated ? my.today : pubs.filter((p) => p.commodity === "electric").reduce((s, p) => s + (p.today || 0), 0);
  const spark = powerHistory.map((p) => p.v);
  // For the first minute after connecting (e.g. a refresh empties the SSE buffer),
  // floor the live count with the server's last-minute count so the rate shows
  // immediately instead of ramping up from ~1; after warmup the buffer is complete.
  const warming = Date.now() - connectedAt < 60_000;
  const liveRate = (s?: string) => perMin(readings, s);
  const rate = warming ? Math.max(liveRate(), health.data?.packets_last_min ?? 0) : liveRate();

  return (
    <Page title="Overview" actions={<Button variant="ghost" icon={<Crosshair size={15} />} onClick={() => onNav("identify")}>Identify</Button>}>
      {/* Row 1 — hero + system status */}
      <div className="grid gap-6 lg:grid-cols-3">
        <div className="lg:col-span-2">
          {ov.error ? <FetchError error={ov.error} onRetry={ov.reload} />
            : ov.data == null ? <Skeleton className="h-48" />
              : my ? <MyHero my={my} power={power} spark={spark} cur={cur} onNav={onNav} />
                : pubs.length > 0 ? <Hero pubs={pubs} power={power} spark={spark} cur={cur} />
                  : <OnboardingHero onNav={onNav} />}
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

      {util.data?.statistic_id && util.data.points.length > 0 && (
        <UtilityMini d={util.data} onNav={onNav} />
      )}

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
            {pubs.length > 0
              ? <div className="grid gap-3 sm:grid-cols-2">{pubs.map((p) => <PubCard key={p.endpoint_id} p={p} cur={cur} onClick={() => onNav("meters", [p.endpoint_id])} />)}</div>
              : my
                ? <EmptyState icon={<Radio size={22} />} title="Not publishing to HA"
                  action={<Button variant="default" onClick={() => onNav("settings")}>Open Settings</Button>}>
                  Your meter's data is still captured and browsable in Usage. To feed it back to Home Assistant,
                  configure an MQTT broker in Settings, then toggle publish on the meter.</EmptyState>
                : <EmptyState icon={<Radio size={22} />} title="Nothing published yet"
                  action={<Button variant="primary" onClick={() => onNav("identify")}>Identify your meter</Button>}>
                  Find which meter is yours, then publish it.</EmptyState>}
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Capture by source" icon={<Radio size={16} />}
            actions={<Button size="sm" variant="ghost" onClick={() => onNav("devices")}>Devices</Button>} />
          <CardBody className="space-y-2">
            {!health.data ? <Skeleton className="h-20" />
              : health.data.sources.length === 0 ? <EmptyState icon={<Radio size={20} />} title="No dongles reporting" action={<Button size="sm" variant="primary" onClick={() => onNav("devices")}>Set up a device</Button>}>Connect an RTL-SDR and start capture to see sources here.</EmptyState>
              : health.data!.sources.map((s) => (
                <div key={s.source} className="flex items-center gap-2 text-small">
                  <Dot tone={s.alive ? "good" : "bad"} />
                  <span className="truncate text-secondary">{srcLabel(s.source)}</span>
                  <span className="ml-auto tabular-nums text-tertiary">{(warming ? Math.max(liveRate(s.source), s.packets_last_min) : liveRate(s.source))}/min</span>
                </div>
              ))}
          </CardBody>
        </Card>
      </div>
    </Page>
  );
}

function UtilityMini({ d, onNav }: { d: UtilitySeries; onNav: (v: View) => void }) {
  const chart = useChartTheme();
  const cur = d.currency || "$";
  const latest = d.points[d.points.length - 1];
  const recent = d.points.slice(-12).map((p) => p.kwh);
  const tracking = latest?.meter_kwh != null && latest.kwh > 0
    ? Math.round((latest.meter_kwh / latest.kwh) * 100) : null;
  return (
    <Card variant="interactive" onClick={() => onNav("utility")}>
      <CardHeader title="Utility bill" icon={<Receipt size={16} />} subtitle={`Billed energy · ${d.period}`}
        actions={<Button size="sm" variant="ghost" icon={<ArrowUpRight size={14} />} onClick={() => onNav("utility")}>View</Button>} />
      <CardBody>
        <div className="flex flex-wrap items-end gap-x-8 gap-y-3">
          <Metric label={`Latest ${d.period}`} value={latest ? `${fmt(latest.kwh, 1)} kWh` : "–"} />
          {latest?.cost != null && <Metric label="Latest cost" value={`${cur}${fmt(latest.cost, 2)}`} tone="text-good" />}
          <Metric label={`Total (${d.bucket_count})`} value={`${fmt(d.total_kwh, 0)} kWh`} />
          {tracking != null && <Metric label="winnow tracking" value={`${tracking}%`} tone={Math.abs(tracking - 100) <= 15 ? "text-good" : "text-gold"} />}
          {recent.length > 1 && <div className="ml-auto h-10 w-40"><Sparkline data={recent} color={chart.gold} height={40} /></div>}
        </div>
      </CardBody>
    </Card>
  );
}

const ago = (ts: string): string => {
  const s = Math.max(0, Math.round((Date.now() - new Date(ts).getTime()) / 1000));
  if (s < 90) return `${s}s ago`;
  if (s < 5400) return `${Math.round(s / 60)}m ago`;
  return `${Math.round(s / 3600)}h ago`;
};

// PublishChip tells the truth about the HA feed: gold only when the worker's
// broker session is up AND a publish actually landed recently.
function PublishChip({ my }: { my: MyMeter }) {
  const p = my.publish;
  if (!p.enabled) return <Badge>not published</Badge>;
  const fresh = p.last_publish_ts != null && Date.now() - new Date(p.last_publish_ts).getTime() < 10 * 60_000;
  if (p.broker_connected && fresh) return <Badge tone="gold"><Radio size={11} /> feeding HA · {ago(p.last_publish_ts!)}</Badge>;
  if (p.broker_connected) return <Badge tone="brand"><Radio size={11} /> publish on — waiting for packets</Badge>;
  return <Badge tone="bad"><Radio size={11} /> not publishing — broker unreachable</Badge>;
}

function MyHero({ my, power, spark, cur, onNav }:
  { my: MyMeter; power: number | null; spark: number[]; cur: string; onNav: (v: View, p?: (string | number)[]) => void }) {
  const unit = my.unit;
  return (
    <Card variant="accent" className="relative overflow-hidden">
      <div className="pointer-events-none absolute inset-0" style={{ background: "radial-gradient(600px 240px at 85% -20%, rgb(var(--brand) / 0.10), transparent 60%)" }} />
      <CardBody>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-h3">{my.label || `Meter #${my.endpoint_id}`}</span>
          {my.is_mine
            ? <Badge tone="brand"><Star size={11} /> your meter</Badge>
            : <Badge tone="gold">likely yours — unconfirmed</Badge>}
          <PublishChip my={my} />
          <span className="ml-auto id-pill">#{my.endpoint_id}</span>
        </div>
        <div className="mt-4 flex items-end gap-2">
          <span className="text-display tabular-nums text-brand">{fmt(my.today, 1)}</span>
          <span className="mb-1.5 text-secondary">{unit} <span className="text-tertiary">today</span></span>
          {power != null && <span className="mb-1.5 ml-auto text-secondary tabular-nums">{fmt(power)} W <span className="text-tertiary">monitored now</span></span>}
        </div>
        {spark.length > 1 && <div className="mt-1 h-12"><Sparkline data={spark} height={48} /></div>}
        <div className="mt-4 flex flex-wrap items-end gap-x-8 gap-y-2 border-t border-border pt-3 text-small">
          <Metric label="This week" value={`${fmt(my.week, 1)} ${unit}`} />
          <Metric label="This month" value={`${fmt(my.month, 1)} ${unit}`} />
          {my.rate != null && <Metric label="Rate" value={`${fmt(my.rate, 2)} ${unit}/h`} />}
          {my.cost_today > 0 && <Metric label="Cost today" value={`${cur}${fmt(my.cost_today, 2)}`} tone="text-good" />}
          <div className="ml-auto">
            <Button size="sm" variant="primary" icon={<BarChart3 size={14} />} onClick={() => onNav("usage", [my.endpoint_id])}>View usage</Button>
          </div>
        </div>
        {!my.calibrated && (
          <div className="mt-2 text-micro text-tertiary">Values are raw counter units — calibrate on Identify to show kWh.</div>
        )}
      </CardBody>
    </Card>
  );
}

function Hero({ pubs, power, spark, cur }: { pubs: PublishedLive[]; power: number | null; spark: number[]; cur: string }) {
  const p = pubs[0];
  return (
    <Card variant="accent" className="relative overflow-hidden">
      <div className="pointer-events-none absolute inset-0" style={{ background: "radial-gradient(600px 240px at 85% -20%, rgb(var(--brand) / 0.10), transparent 60%)" }} />
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
      <div className="pointer-events-none absolute inset-0" style={{ background: "radial-gradient(600px 240px at 85% -20%, rgb(var(--brand) / 0.08), transparent 60%)" }} />
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
  const label = a.kind === "dropout" ? "Dropout" : a.kind === "stuck" ? "Stuck odometer"
    : a.kind === "reference_stale" ? "Reference feed down" : "Source down";
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
