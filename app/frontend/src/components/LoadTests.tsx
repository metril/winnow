import { useEffect, useState } from "react";
import { api, TestWindow } from "../api";
import { useLive } from "../App";
import { shortTs } from "../util";
import { Card, SectionTitle, Button, Input, Badge, Dot, EmptyState, Skeleton } from "../ui";
import { CorrelationBar } from "./charts";

export default function LoadTests() {
  const { tick } = useLive();
  const [tests, setTests] = useState<TestWindow[] | null>(null);
  const [label, setLabel] = useState("");
  const [combined, setCombined] = useState<any>(null);

  const load = () => api.tests().then(setTests);
  const loadCombined = () => api.combined().then(setCombined).catch(() => setCombined(null));
  useEffect(() => { load(); loadCombined(); /* eslint-disable-next-line */ }, [tick]);

  const running = tests?.find((t) => !t.end_ts);
  const start = () => api.startTest(label || "load test").then(() => { setLabel(""); load(); });
  const stop = (id: number) => api.stopTest(id).then(() => { load(); loadCombined(); });

  return (
    <div className="space-y-4">
      <Card>
        <SectionTitle sub="Switch a known appliance on for a few minutes, then off. The meter whose consumption spikes with it is yours — repeat to be sure.">
          Run a load test
        </SectionTitle>
        {running ? (
          <div className="flex flex-wrap items-center gap-3">
            <Badge tone="bad"><span className="h-2 w-2 animate-pulse2 rounded-full bg-bad" /> recording</Badge>
            <span className="text-sm">“{running.label}” since <span className="mono">{shortTs(running.start_ts)}</span></span>
            <Button variant="danger" success="Test stopped" onClick={() => stop(running.id)}>Stop test</Button>
          </div>
        ) : (
          <div className="flex flex-wrap items-end gap-2">
            <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. kettle + oven" className="max-w-xs" />
            <Button variant="primary" success="Recording started" onClick={start}>Start test</Button>
          </div>
        )}
      </Card>

      {combined?.ranking?.length > 0 && (
        <Card>
          <SectionTitle sub={`Aggregated across ${combined.tests?.length || 0} completed tests — the meter that wins the most is almost certainly yours.`}>
            Combined ranking
          </SectionTitle>
          <table className="w-full text-sm">
            <thead><tr className="text-left text-faint">
              <th className="py-1.5 font-medium">meter</th><th className="font-medium">commodity</th>
              <th className="font-medium text-right">avg r</th><th className="font-medium text-right">wins</th>
            </tr></thead>
            <tbody>
              {combined.ranking.slice(0, 10).map((r: any, i: number) => (
                <tr key={r.endpoint_id} className={"border-t border-border/50 " + (i === 0 ? "text-gold" : "")}>
                  <td className="py-1.5 mono">#{r.endpoint_id}</td>
                  <td className="text-muted">{r.commodity}</td>
                  <td className="text-right tabular-nums">{r.avg_r != null ? r.avg_r.toFixed(2) : "–"}</td>
                  <td className="text-right tabular-nums">{r.wins}/{r.tests_total}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}

      <Card>
        <SectionTitle>Test history</SectionTitle>
        {!tests ? <Skeleton className="h-20" /> : tests.length === 0 ? <EmptyState>No tests yet.</EmptyState> : (
          <div className="space-y-2">{tests.map((t) => <TestRow key={t.id} t={t} onChange={() => { load(); loadCombined(); }} />)}</div>
        )}
      </Card>
    </div>
  );
}

function TestRow({ t, onChange }: { t: TestWindow; onChange: () => void }) {
  const [open, setOpen] = useState(false);
  const [rank, setRank] = useState<any>(null);
  const toggle = () => {
    if (!open && !rank) api.correlation(t.id).then((r) => setRank(r));
    setOpen(!open);
  };
  return (
    <div className="rounded-lg border border-border">
      <div className="flex items-center justify-between gap-2 px-3 py-2">
        <button onClick={toggle} className="flex flex-1 items-center gap-2 text-left text-sm">
          <Dot ok={!!t.end_ts} />
          <span className="font-medium">{t.label}</span>
          <span className="mono text-xs text-faint">{shortTs(t.start_ts)} → {t.end_ts ? shortTs(t.end_ts) : "running"}</span>
          <Badge>{t.source}</Badge>
        </button>
        <Button size="sm" variant="ghost" onClick={toggle}>{open ? "Hide" : "Ranking"}</Button>
        <Button size="sm" variant="danger" success="Deleted" onClick={() => api.deleteTest(t.id).then(onChange)}>Delete</Button>
      </div>
      {open && (
        <div className="border-t border-border px-3 py-2">
          {!rank ? <Skeleton className="h-10" /> : (
            <table className="w-full text-sm">
              <tbody>
                {(rank.ranking || []).slice(0, 6).map((r: any, i: number) => (
                  <tr key={r.endpoint_id} className={i === 0 ? "text-gold" : ""}>
                    <td className="py-1 mono">#{r.endpoint_id}</td>
                    <td className="text-muted">{r.commodity}</td>
                    <td className="w-40"><CorrelationBar r={r.r} /></td>
                    <td className="text-right tabular-nums text-muted">×{r.score?.toFixed?.(1) ?? r.score} rate</td>
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
