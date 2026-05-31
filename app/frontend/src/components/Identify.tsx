import { useEffect, useState } from "react";
import { api, CorrRow } from "../api";
import { useLive } from "../App";
import { fmt } from "../util";
import { OverlayChart, CorrelationBar } from "./charts";

// Identify: correlate every meter against the plug's power profile.
export default function Identify() {
  const { tick, lastPower } = useLive();
  const [hours, setHours] = useState(2);
  const [data, setData] = useState<any>(null);
  const [ref, setRef] = useState<{ bucket: string; value: number }[]>([]);
  const [series, setSeries] = useState<Record<string, { bucket: string; value: number }[]>>({});
  const [auto, setAuto] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);

  const load = async () => {
    try {
      const d = await api.identify(hours);
      setData(d);
      setErr(null);
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
  const noEntity = data && !data.entity;

  return (
    <div>
      <div className="panel">
        <h2>Identify your meter</h2>
        <p className="muted">
          Switch a known load (space heater, kettle) on and off through your Home Assistant smart
          plug. winnow ranks every meter by how tightly its consumption tracks the plug's power —
          the top match with a high correlation is yours.
        </p>
        {noEntity && (
          <div className="error">No reference plug configured. Set one in <strong>Settings → Integrations</strong>.</div>
        )}
        <div className="controls">
          <label>analyze last</label>
          <select value={hours} onChange={(e) => setHours(+e.target.value)}>
            <option value={1}>1h</option><option value={2}>2h</option>
            <option value={6}>6h</option><option value={24}>24h</option>
          </select>
          <button className="btn" onClick={load}>Analyze</button>
          {lastPower !== null && <span className="badge">plug now: {fmt(lastPower)} W</span>}
          {data?.entity && <span className="muted">plug: {data.entity}</span>}
        </div>
        {err && <div className="error">{err}</div>}
      </div>

      {ref.length > 0 && (
        <div className="panel">
          <h3>Plug power vs top candidates (the spike should line up)</h3>
          <OverlayChart reference={ref} meters={series} />
        </div>
      )}

      <div className="panel" style={{ padding: 0 }}>
        <table>
          <thead><tr>
            <th>#</th><th>meter</th><th>commodity</th><th>correlation r</th>
            <th className="num">score</th><th className="num">window Δ</th>
            <th className="num">plug Wh</th><th className="num">pkts</th><th>actions</th>
          </tr></thead>
          <tbody>
            {ranking.slice(0, 15).map((r, i) => (
              <tr key={r.endpoint_id} className={i === 0 && (r.r ?? 0) > 0.5 ? "mine" : ""}>
                <td>{i + 1}</td>
                <td className={i === 0 ? "spike" : ""}>{r.endpoint_id}</td>
                <td>{r.commodity}</td>
                <td style={{ minWidth: 120 }}><CorrelationBar r={r.r} /></td>
                <td className="num">{fmt(r.score, 1)}×</td>
                <td className="num">{fmt(r.window_delta)}</td>
                <td className="num">{fmt(r.plug_energy_wh, 0)}</td>
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
          <h3>Best across all auto windows ({auto.tests.length} windows)</h3>
          <p className="muted">winnow opens a window automatically whenever the plug turns on. The
            meter that wins repeatedly is almost certainly yours.</p>
          <table>
            <thead><tr><th>meter</th><th className="num">avg r</th><th className="num">wins</th><th className="num">avg score</th></tr></thead>
            <tbody>
              {auto.ranking.slice(0, 8).map((r: any, i: number) => (
                <tr key={r.endpoint_id} className={i === 0 ? "mine" : ""}>
                  <td className={i === 0 ? "spike" : ""}>{r.endpoint_id} <span className="muted">{r.commodity}</span></td>
                  <td className="num">{r.avg_r != null ? r.avg_r.toFixed(2) : "–"}</td>
                  <td className="num">{r.wins}/{r.tests_total}</td>
                  <td className="num">{fmt(r.avg_score, 1)}×</td>
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
