import { useEffect, useState } from "react";
import { api, CorrRow } from "../api";
import { useLive } from "../App";
import { fmt } from "../util";
import { OverlayChart, CorrelationBar } from "./charts";

// Identify: rank every meter by how well it tracks your TOTAL monitored power.
export default function Identify() {
  const { tick, lastPower } = useLive();
  const [hours, setHours] = useState(6);
  const [data, setData] = useState<any>(null);
  const [ref, setRef] = useState<{ bucket: string; value: number }[]>([]);
  const [series, setSeries] = useState<Record<string, { bucket: string; value: number }[]>>({});
  const [auto, setAuto] = useState<any>(null);
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
      setAuto(await api.identifyAuto());
    } catch (e) { setErr(String(e)); }
  };
  useEffect(() => { load(); /* eslint-disable-next-line */ }, [hours, tick]);

  const ranking: CorrRow[] = data?.ranking || [];
  const noSet = data && !(data.monitored_entities?.length);
  const floor = data?.monitored_floor_w;

  const apply = (r: CorrRow) =>
    api.patchMeter(r.endpoint_id, { pub_multiplier: r.suggested_multiplier, pub_unit: "kWh" }).then(load);

  return (
    <div>
      <div className="panel">
        <h2>Identify your meter</h2>
        <p className="muted">
          Ranked by how tightly each meter's consumption tracks your <strong>total
          monitored power</strong> (the sum of your HA-monitored devices). The real
          meter shows a high correlation and a clean regression — which also
          calibrates its units and estimates your unmonitored baseline. A meter
          that can't cover your minimum monitored power sinks.
        </p>
        {noSet && <div className="error">No monitored devices configured. Set them in <strong>Settings → Monitored consumption</strong>.</div>}
        <div className="controls">
          <label>analyze last</label>
          <select value={hours} onChange={(e) => setHours(+e.target.value)}>
            <option value={1}>1h</option><option value={6}>6h</option>
            <option value={24}>24h</option><option value={72}>3d</option>
          </select>
          <button className="btn" onClick={load}>Analyze</button>
          {floor != null && <span className="badge">your floor ≈ {fmt(floor)} W</span>}
          {lastPower !== null && <span className="badge">monitored now: {fmt(lastPower)} W</span>}
        </div>
        {err && <div className="error">{err}</div>}
      </div>

      {ref.length > 0 && (
        <div className="panel">
          <h3>Total monitored power vs top candidates</h3>
          <OverlayChart reference={ref} meters={series} />
        </div>
      )}

      <div className="panel" style={{ padding: 0 }}>
        <table>
          <thead><tr>
            <th>#</th><th>meter</th><th>commodity</th><th>correlation r</th>
            <th className="num">calibration</th><th className="num">baseline</th>
            <th>floor</th><th className="num">pkts</th><th>actions</th>
          </tr></thead>
          <tbody>
            {ranking.slice(0, 15).map((r, i) => (
              <tr key={r.endpoint_id} className={i === 0 && (r.r ?? 0) > 0.5 ? "mine" : ""}>
                <td>{i + 1}</td>
                <td className={i === 0 ? "spike" : ""}>{r.endpoint_id}</td>
                <td>{r.commodity}</td>
                <td style={{ minWidth: 120 }}><CorrelationBar r={r.r} /></td>
                <td className="num">
                  {r.suggested_multiplier
                    ? <button className="mini" title="set pub multiplier (kWh/unit)" onClick={() => apply(r)}>×{r.suggested_multiplier.toPrecision(3)}</button>
                    : <span className="muted">–</span>}
                </td>
                <td className="num">{r.baseline_w != null ? fmt(r.baseline_w) + " W" : "–"}</td>
                <td>{r.floor_ok == null ? <span className="muted">–</span> : r.floor_ok ? "✓" : <span className="error">✗</span>}</td>
                <td className="num">{r.window_packets}</td>
                <td><RowActions id={r.endpoint_id} onChange={load} /></td>
              </tr>
            ))}
            {!ranking.length && <tr><td colSpan={9} className="muted">no data in window</td></tr>}
          </tbody>
        </table>
      </div>

      {auto?.ranking?.length > 0 && (
        <div className="panel">
          <h3>Best across all auto windows ({auto.tests.length})</h3>
          <table>
            <thead><tr><th>meter</th><th className="num">avg r</th><th className="num">wins</th></tr></thead>
            <tbody>
              {auto.ranking.slice(0, 8).map((r: any, i: number) => (
                <tr key={r.endpoint_id} className={i === 0 ? "mine" : ""}>
                  <td className={i === 0 ? "spike" : ""}>{r.endpoint_id} <span className="muted">{r.commodity}</span></td>
                  <td className="num">{r.avg_r != null ? r.avg_r.toFixed(2) : "–"}</td>
                  <td className="num">{r.wins}/{r.tests_total}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function RowActions({ id, onChange }: { id: number; onChange: () => void }) {
  return (
    <span style={{ whiteSpace: "nowrap" }}>
      <button className="btn alt" onClick={() => api.patchMeter(id, { is_mine: true, is_candidate: true }).then(onChange)}>Track</button>{" "}
      <button className="btn gold" onClick={() => api.patchMeter(id, { is_mine: true, publish: true }).then(onChange)}>Publish</button>
    </span>
  );
}
