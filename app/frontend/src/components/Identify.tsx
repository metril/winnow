import { useEffect, useState } from "react";
import { api, CorrRow } from "../api";
import { useLive } from "../App";
import { fmt } from "../util";
import { Card, SectionTitle, Button, Select, Badge, EmptyState } from "../ui";
import { OverlayChart, CorrelationBar } from "./charts";

export default function Identify() {
  const { tick, lastPower } = useLive();
  const [hours, setHours] = useState(6);
  const [data, setData] = useState<any>(null);
  const [ref, setRef] = useState<{ bucket: string; value: number }[]>([]);
  const [series, setSeries] = useState<Record<string, { bucket: string; value: number }[]>>({});
  const [err, setErr] = useState<string | null>(null);

  const load = async () => {
    try {
      const d = await api.identify(hours);
      setData(d); setErr(null);
      if (d.ranking?.length) {
        const top = d.ranking.slice(0, 3).map((r: CorrRow) => r.endpoint_id);
        const [rs, ss] = await Promise.all([
          api.referenceSeries(d.start, d.end),
          api.series(top, `since=${d.start}&bucket=5m&mode=delta`),
        ]);
        setRef(rs); setSeries(ss);
      }
    } catch (e) { setErr(String(e)); }
  };
  useEffect(() => { load(); /* eslint-disable-next-line */ }, [hours, tick]);

  const ranking: CorrRow[] = data?.ranking || [];
  const noSet = data && !(data.monitored_entities?.length);
  const floor = data?.monitored_floor_w;
  const apply = (r: CorrRow) => api.patchMeter(r.endpoint_id, { pub_multiplier: r.suggested_multiplier, pub_unit: "kWh" }).then(load);

  return (
    <div className="space-y-4">
      <Card>
        <SectionTitle
          right={<>
            <Select value={hours} onChange={(e) => setHours(+e.target.value)} className="w-24">
              <option value={1}>1h</option><option value={6}>6h</option><option value={24}>24h</option><option value={72}>3d</option>
            </Select>
            <Button variant="primary" onClick={load} success="Analyzed">Analyze</Button>
          </>}
          sub="Ranked by how tightly each meter tracks your total monitored power. The real meter shows a high correlation and a clean regression — which also calibrates its units and estimates your unmonitored baseline.">
          Identify your meter
        </SectionTitle>
        <div className="flex flex-wrap items-center gap-2">
          {floor != null && <Badge tone="gold">floor ≈ {fmt(floor)} W</Badge>}
          {lastPower !== null && <Badge tone="brand">monitored now {fmt(lastPower)} W</Badge>}
          {noSet && <Badge tone="bad">no monitored devices — set them in Settings</Badge>}
        </div>
      </Card>

      {ref.length > 0 && (
        <Card>
          <SectionTitle sub="Yellow = your total monitored power; lines = top candidate meters' per-minute usage.">Overlay</SectionTitle>
          <OverlayChart reference={ref} meters={series} />
        </Card>
      )}

      <Card className="overflow-hidden p-0">
        <table className="w-full text-sm">
          <thead className="bg-surface2/60 text-left text-xs text-muted">
            <tr>
              <th className="px-3 py-2 font-medium">#</th><th className="px-3 py-2 font-medium">meter</th>
              <th className="px-3 py-2 font-medium">commodity</th><th className="px-3 py-2 font-medium w-44">correlation</th>
              <th className="px-3 py-2 font-medium text-right">calibration</th><th className="px-3 py-2 font-medium text-right">baseline</th>
              <th className="px-3 py-2 font-medium text-center">floor</th><th className="px-3 py-2 font-medium text-right">pkts</th>
              <th className="px-3 py-2 font-medium">actions</th>
            </tr>
          </thead>
          <tbody>
            {ranking.slice(0, 15).map((r, i) => (
              <tr key={r.endpoint_id} className={"border-t border-border/50 " + (i === 0 && (r.r ?? 0) > 0.5 ? "bg-brand/5" : "")}>
                <td className="px-3 py-2 text-faint">{i + 1}</td>
                <td className={"px-3 py-2 mono " + (i === 0 ? "text-brand font-medium" : "")}>#{r.endpoint_id}</td>
                <td className="px-3 py-2 text-muted">{r.commodity}</td>
                <td className="px-3 py-2"><CorrelationBar r={r.r} /></td>
                <td className="px-3 py-2 text-right">
                  {r.suggested_multiplier
                    ? <Button size="sm" title="set publish multiplier (kWh/unit)" onClick={() => apply(r)} success="Multiplier applied">×{r.suggested_multiplier.toPrecision(3)}</Button>
                    : <span className="text-faint">–</span>}
                </td>
                <td className="px-3 py-2 text-right tabular-nums text-muted">{r.baseline_w != null ? `${fmt(r.baseline_w)} W` : "–"}</td>
                <td className="px-3 py-2 text-center">{r.floor_ok == null ? <span className="text-faint">–</span> : r.floor_ok ? <span className="text-good">✓</span> : <span className="text-bad">✗</span>}</td>
                <td className="px-3 py-2 text-right tabular-nums text-muted">{r.window_packets}</td>
                <td className="px-3 py-2"><RowActions id={r.endpoint_id} onChange={load} /></td>
              </tr>
            ))}
            {!ranking.length && <tr><td colSpan={9}><EmptyState>{err || "No data in window."}</EmptyState></td></tr>}
          </tbody>
        </table>
      </Card>
    </div>
  );
}

function RowActions({ id, onChange }: { id: number; onChange: () => void }) {
  return (
    <div className="flex gap-1.5">
      <Button size="sm" success="Tracked" onClick={() => api.patchMeter(id, { is_mine: true, is_candidate: true }).then(onChange)}>Track</Button>
      <Button size="sm" variant="gold" success="Publishing to HA" onClick={() => api.patchMeter(id, { is_mine: true, publish: true }).then(onChange)}>Publish</Button>
    </div>
  );
}
