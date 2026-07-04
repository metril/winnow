import { useState } from "react";
import { Zap, Square, Play, Trash2, Trophy, ChevronDown, ChevronRight } from "lucide-react";
import { api, TestWindow } from "../api";
import { useLiveMeta } from "../live";
import { useFetch } from "../fetch";
import { shortTs } from "../util";
import { Page } from "./shell";
import { Card, CardHeader, CardBody, Button, Input, Field, Badge, Dot, EmptyState, Skeleton, Table, Th, Td, InfoHint } from "../ui";
import { ConfidenceBar } from "./charts";

export default function LoadTests() {
  const { configVersion } = useLiveMeta();
  const [label, setLabel] = useState("");
  const [knownW, setKnownW] = useState("");
  const tests = useFetch(api.tests, [configVersion]);
  const combined = useFetch(() => api.combined().catch(() => null), [configVersion]);

  const list = tests.data || [];
  const running = list.find((t) => !t.end_ts);
  const refresh = () => { tests.reload(); combined.reload(); };
  const start = () => {
    const w = parseFloat(knownW);
    return api.startTest(label || "load test", w > 0 ? { known_load_w: w } : undefined)
      .then(() => { setLabel(""); setKnownW(""); refresh(); });
  };
  const stop = (id: number) => api.stopTest(id).then(refresh);

  return (
    <Page title="Load tests">
      <Card>
        <CardHeader title="Run a load test" subtitle="Switch a known appliance on for a few minutes, then off — the meter that spikes with it is yours." icon={<Zap size={16} />} />
        <CardBody>
          {running ? (
            <div className="flex flex-wrap items-center gap-3">
              <Badge tone="bad"><span className="h-2 w-2 animate-pulse2 rounded-full bg-bad" /> recording</Badge>
              <span className="text-small">“{running.label}” since <span className="mono text-tertiary">{shortTs(running.start_ts)}</span></span>
              <Button variant="danger" icon={<Square size={14} />} onClick={() => stop(running.id)} success="Test stopped">Stop test</Button>
            </div>
          ) : (
            <div className="flex flex-wrap items-end gap-2">
              <Field label="what you're switching"><Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. kettle + oven" className="max-w-xs" /></Field>
              <Field label="known load (W, optional)" hint="if you know the wattage, winnow calibrates directly">
                <Input value={knownW} onChange={(e) => setKnownW(e.target.value)} placeholder="e.g. 1500" className="w-40" />
              </Field>
              <Button variant="primary" icon={<Play size={14} />} onClick={start} success="Recording started">Start test</Button>
            </div>
          )}
        </CardBody>
      </Card>

      {combined.data?.ranking?.length > 0 && (
        <Card>
          <CardHeader title={<span className="inline-flex items-center gap-2"><Trophy size={16} className="text-gold" /> Combined ranking</span>}
            subtitle={combinedSubtitle(combined.data)} />
          <Table>
            <thead><tr><Th>meter</Th><Th>commodity</Th><Th className="w-40">confidence</Th><Th num>avg r</Th>
              <Th num>utility ×<InfoHint>The multiplier (kWh per meter count) implied by your utility bill, and — when the bill is hourly/daily — the per-bucket correlation. A meter whose bill multiplier is consistent and agrees with the load-test calibration is very likely yours.</InfoHint></Th>
              <Th num>wins</Th></tr></thead>
            <tbody>
              {combined.data.ranking.slice(0, 10).map((r: any, i: number) => (
                <tr key={r.endpoint_id} className={"border-b border-border/60 " + (i === 0 ? "bg-gold/5" : "")}>
                  <Td><span className="id-pill">#{r.endpoint_id}</span></Td>
                  <Td className="text-secondary">{r.commodity}</Td>
                  <Td>{r.confidence != null ? <ConfidenceBar r={r.confidence} title={confTitle(r)} /> : <span className="text-tertiary">–</span>}</Td>
                  <Td num>{r.avg_r != null ? r.avg_r.toFixed(2) : "–"}</Td>
                  <Td num className="text-secondary">{r.utility_multiplier != null
                    ? <span title={`${r.utility_buckets_covered || 0} billing bucket(s)`}>×{Number(r.utility_multiplier).toPrecision(3)}{r.utility_r != null ? ` · r${Number(r.utility_r).toFixed(2)}` : ""}</span>
                    : <span className="text-tertiary">–</span>}</Td>
                  <Td num className={i === 0 ? "text-gold" : ""}>{r.tests_total > 0 ? `${r.wins}/${r.tests_total}` : "–"}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}

      <Card>
        <CardHeader title="Test history" />
        <CardBody className="space-y-2">
          {!tests.data ? <Skeleton className="h-16" /> : list.length === 0 ? <EmptyState icon={<Zap size={20} />}>No tests yet.</EmptyState>
            : list.map((t) => <TestRow key={t.id} t={t} onChange={refresh} />)}
        </CardBody>
      </Card>
    </Page>
  );
}

function combinedSubtitle(d: any): string {
  const n = d.tests?.length || 0;
  const hasUtil = (d.ranking || []).some((r: any) => r.utility_multiplier != null);
  if (n > 0 && hasUtil) return `Across ${n} completed test(s) plus your utility bill — the meter that agrees with both is almost certainly yours.`;
  if (n > 0) return `Across ${n} completed test(s) — the meter that wins most is almost certainly yours.`;
  if (hasUtil) return "From your utility bill — meters whose counter tracks your billed energy. Run load tests to confirm.";
  return "Run load tests (or connect a utility bill in Settings) to rank candidates.";
}

// confTitle builds a hover breakdown for a combined-ranking row.
function confTitle(r: any): string {
  return [
    r.suggested_multiplier != null ? `regression ×${Number(r.suggested_multiplier).toPrecision(3)}` : null,
    r.anchor_multiplier != null ? `known-load ×${Number(r.anchor_multiplier).toPrecision(3)}` : null,
    r.utility_multiplier != null ? `utility ×${Number(r.utility_multiplier).toPrecision(3)}` : null,
    r.multiplier_cov != null ? `stability CoV ${Number(r.multiplier_cov).toFixed(2)}` : null,
  ].filter(Boolean).join(" · ");
}

function TestRow({ t, onChange }: { t: TestWindow; onChange: () => void }) {
  const [open, setOpen] = useState(false);
  const [rank, setRank] = useState<any>(null);
  const toggle = () => { if (!open && !rank) api.correlation(t.id).then(setRank); setOpen(!open); };
  return (
    <div className="rounded-xl border border-border">
      <div className="flex items-center gap-2 px-3 py-2">
        <button onClick={toggle} className="flex flex-1 items-center gap-2 text-left text-small">
          {open ? <ChevronDown size={15} className="text-tertiary" /> : <ChevronRight size={15} className="text-tertiary" />}
          <Dot tone={t.end_ts ? "good" : "warn"} />
          <span className="font-medium">{t.label}</span>
          <span className="mono text-micro text-tertiary">{shortTs(t.start_ts)} → {t.end_ts ? shortTs(t.end_ts) : "running"}</span>
          <Badge>{t.source}</Badge>
          {t.known_load_w != null && <Badge tone="gold">{t.known_load_w} W known</Badge>}
        </button>
        <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} onClick={() => api.deleteTest(t.id).then(onChange)} success="Deleted" />
      </div>
      {open && (
        <div className="border-t border-border px-3 py-2">
          {!rank ? <Skeleton className="h-10" /> : (
            <table className="w-full text-small">
              <tbody>
                {(rank.ranking || []).slice(0, 6).map((r: any, i: number) => (
                  <tr key={r.endpoint_id}>
                    <td className="py-1"><span className="id-pill">#{r.endpoint_id}</span></td>
                    <td className="text-tertiary">{r.commodity}</td>
                    <td className="w-40"><ConfidenceBar r={r.confidence ?? r.r} /></td>
                    <td className={"text-right tabular-nums " + (i === 0 ? "text-gold" : "text-tertiary")}>×{r.score?.toFixed?.(1) ?? r.score} rate</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  );
}
