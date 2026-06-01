import { api, useAsync, PublishedLive, Anomaly } from "../api";
import { useLive } from "../App";
import { fmt } from "../util";
import { Card, SectionTitle, Stat, Badge, Dot, EmptyState, Button, Skeleton } from "../ui";

export default function Overview({ onNav }: { onNav: (v: string) => void }) {
  const { tick, lastPower } = useLive();
  const ov = useAsync(api.overview, "ov" + tick);
  const health = useAsync(api.health, "h" + tick);

  const pubs = ov.data?.published || [];
  const anomalies = ov.data?.anomalies || [];
  const cur = ov.data?.currency || "$";
  const totalCost = pubs.reduce((s, p) => s + (p.cost_today || 0), 0);
  const totalToday = pubs.filter((p) => p.commodity === "electric").reduce((s, p) => s + (p.today || 0), 0);

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat label="Monitored now" tone="gold" value={lastPower != null ? `${fmt(lastPower)} W` : "–"} sub="sum of HA devices" />
        <Stat label="Published meters" tone="brand" value={pubs.length} sub="feeding Home Assistant" />
        <Stat label="Today (electric)" value={`${fmt(totalToday, 1)} kWh`} sub="across published" />
        <Stat label="Est. cost today" tone="good" value={ov.data?.cost_per_kwh ? `${cur}${fmt(totalCost, 2)}` : "–"}
          sub={ov.data?.cost_per_kwh ? `@ ${cur}${ov.data.cost_per_kwh}/kWh` : "set tariff in Settings"} />
      </div>

      {anomalies.length > 0 && (
        <Card className="border-bad/30">
          <SectionTitle sub="Based on tracked meters and capture health.">Alerts</SectionTitle>
          <div className="flex flex-wrap gap-2">{anomalies.map((a, i) => <AnomalyChip key={i} a={a} />)}</div>
        </Card>
      )}

      <Card>
        <SectionTitle right={<Button size="sm" variant="ghost" onClick={() => onNav("identify")}>Identify →</Button>}
          sub="Live rate, today's consumption and estimated cost for each meter you publish to Home Assistant.">
          Your meters
        </SectionTitle>
        {ov.data == null ? (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">{[0, 1, 2].map((i) => <Skeleton key={i} className="h-28" />)}</div>
        ) : pubs.length === 0 ? (
          <EmptyState>No meters published yet — find yours in <button className="text-brand underline" onClick={() => onNav("identify")}>Identify</button>, then Publish.</EmptyState>
        ) : (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {pubs.map((p) => <PubCard key={p.endpoint_id} p={p} currency={cur} onNav={onNav} />)}
          </div>
        )}
      </Card>

      <Card>
        <SectionTitle right={<Button size="sm" variant="ghost" onClick={() => onNav("devices")}>Devices →</Button>}>Capture health</SectionTitle>
        {!health.data ? <Skeleton className="h-10" /> : (
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={health.data.alive ? "good" : "bad"}><Dot ok={health.data.alive} /> {health.data.alive ? "receiving" : "down"}</Badge>
            <Badge>{health.data.packets_last_min}/min</Badge>
            <Badge>{health.data.unique_meters} meters seen</Badge>
            <span className="mx-1 text-faint">·</span>
            {health.data.sources.map((s) => (
              <Badge key={s.source} tone={s.alive ? "default" : "bad"}>
                <Dot ok={s.alive} /> <span className="mono">{s.source}</span> {s.packets_last_min}/min
              </Badge>
            ))}
          </div>
        )}
      </Card>
    </div>
  );
}

function PubCard({ p, currency, onNav }: { p: PublishedLive; currency: string; onNav: (v: string) => void }) {
  return (
    <button onClick={() => onNav("meters")} className="card p-4 text-left transition hover:border-borderlt">
      <div className="flex items-center justify-between">
        <div className="font-medium">{p.name || `meter ${p.endpoint_id}`}</div>
        <Badge tone={p.commodity === "electric" ? "gold" : "brand"}>{p.commodity}</Badge>
      </div>
      <div className="mt-3 flex items-end gap-1">
        <span className="text-3xl font-semibold tabular-nums text-brand">{p.rate != null ? fmt(p.rate, 2) : "–"}</span>
        <span className="mb-1 text-sm text-muted">{p.unit || ""}/h</span>
      </div>
      <div className="mt-2 flex items-center justify-between text-xs text-muted">
        <span>today {fmt(p.today, 2)} {p.unit}</span>
        {p.cost_today > 0 && <span className="text-good">{currency}{fmt(p.cost_today, 2)}</span>}
      </div>
      <div className="mono mt-1 text-[11px] text-faint">#{p.endpoint_id}</div>
    </button>
  );
}

function AnomalyChip({ a }: { a: Anomaly }) {
  const label = a.kind === "dropout" ? "Dropout" : a.kind === "stuck" ? "Stuck" : "Source down";
  const who = a.source ? a.source : a.endpoint_id ? `#${a.endpoint_id}` : "";
  return <Badge tone="bad" className="whitespace-normal"><strong>{label}</strong> <span className="mono">{who}</span> <span className="text-muted">— {a.detail.replace(/[TZ]/g, " ").trim()}</span></Badge>;
}
