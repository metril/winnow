import { useEffect, useState } from "react";
import { api, CorrRow, TestWindow } from "../api";
import { fmt, isoToLocal, localToIso, shortTs } from "../util";
import MeterDetail from "./MeterDetail";

export default function LoadTest() {
  const [tests, setTests] = useState<TestWindow[]>([]);
  const [running, setRunning] = useState<TestWindow | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [label, setLabel] = useState("space heater");

  const refresh = async () => {
    const t = await api.tests();
    setTests(t);
    setRunning(t.find((x) => !x.end_ts) || null);
    if (selected === null && t.length) setSelected(t[0].id);
  };
  useEffect(() => { refresh(); /* eslint-disable-next-line */ }, []);

  return (
    <div>
      <div className="panel">
        <h2>Run a load test</h2>
        <p className="muted">
          Switch on a big known load (oven, space heater), press Start, wait
          1–2h, switch off, press Stop. The meter whose usage spikes during the
          window and returns to baseline after is almost certainly yours.
        </p>
        <div className="controls">
          <label>label</label>
          <input type="text" value={label} onChange={(e) => setLabel(e.target.value)} />
          {running ? (
            <button className="btn danger" onClick={async () => { await api.stopTest(running.id); refresh(); }}>
              ⏹ Stop test ({running.label})
            </button>
          ) : (
            <button className="btn" onClick={async () => { const t = await api.startTest(label); setSelected(t.id); refresh(); }}>
              ▶ Start test (now)
            </button>
          )}
        </div>
        {running && <p className="spike">● Test "{running.label}" running since {shortTs(running.start_ts)}</p>}
        <AfterTheFact onCreate={refresh} defaultLabel={label} />
      </div>

      <div className="grid2">
        <div className="panel">
          <h2>Test windows</h2>
          <table>
            <thead><tr><th>label</th><th>start</th><th>end</th><th></th></tr></thead>
            <tbody>
              {tests.map((t) => (
                <tr key={t.id} className={t.id === selected ? "selected" : ""}
                  onClick={() => setSelected(t.id)}>
                  <td>{t.label}</td>
                  <td>{shortTs(t.start_ts)}</td>
                  <td>{t.end_ts ? shortTs(t.end_ts) : <span className="spike">running</span>}</td>
                  <td><button className="btn alt" onClick={(e) => { e.stopPropagation(); api.deleteTest(t.id).then(refresh); }}>✕</button></td>
                </tr>
              ))}
              {!tests.length && <tr><td colSpan={4} className="muted">no tests yet</td></tr>}
            </tbody>
          </table>
        </div>
        <Combined />
      </div>

      {selected !== null && <Correlation testId={selected} onLock={refresh} />}
    </div>
  );
}

function AfterTheFact({ onCreate, defaultLabel }: { onCreate: () => void; defaultLabel: string }) {
  const [lbl, setLbl] = useState(defaultLabel);
  const now = new Date();
  const [start, setStart] = useState(isoToLocal(new Date(now.getTime() - 2 * 3600_000).toISOString()));
  const [end, setEnd] = useState(isoToLocal(now.toISOString()));
  return (
    <details style={{ marginTop: 10 }}>
      <summary className="muted" style={{ cursor: "pointer" }}>…or enter a past window (after the fact)</summary>
      <div className="controls" style={{ marginTop: 10 }}>
        <input type="text" value={lbl} onChange={(e) => setLbl(e.target.value)} />
        <input type="datetime-local" value={start} onChange={(e) => setStart(e.target.value)} />
        <span className="muted">→</span>
        <input type="datetime-local" value={end} onChange={(e) => setEnd(e.target.value)} />
        <button className="btn" onClick={async () => {
          await api.createTest(lbl, localToIso(start), localToIso(end)); onCreate();
        }}>Add window</button>
      </div>
    </details>
  );
}

function Correlation({ testId, onLock }: { testId: number; onLock: () => void }) {
  const [data, setData] = useState<any>(null);
  const load = () => api.correlation(testId).then(setData);
  useEffect(() => { load(); /* eslint-disable-next-line */ }, [testId]);
  if (!data) return null;
  const ranking: CorrRow[] = data.ranking;
  const test = data.test;
  const top = ranking.slice(0, 3);

  return (
    <div className="panel">
      <h2>Correlation — "{test.label}" ({shortTs(test.start_ts)} → {shortTs(data.end_used)})</h2>
      <p className="muted">Ranked by in-window rate ÷ baseline rate. A clean winner well above the rest is your meter.</p>
      <table>
        <thead><tr>
          <th>rank</th><th>meter</th><th>commodity</th>
          <th className="num">score</th><th className="num">window Δ</th>
          <th className="num">in-window/hr</th><th className="num">baseline/hr</th>
          <th className="num">pkts</th><th></th>
        </tr></thead>
        <tbody>
          {ranking.slice(0, 12).map((r, i) => (
            <tr key={r.endpoint_id} className={i === 0 ? "mine" : ""}>
              <td>{i + 1}</td>
              <td className={i === 0 ? "spike" : ""}>{r.endpoint_id}</td>
              <td>{r.commodity}</td>
              <td className="num">{r.score >= 9999 ? "∞" : fmt(r.score, 1)}×</td>
              <td className="num">{fmt(r.window_delta)}</td>
              <td className="num">{fmt(r.window_rate, 1)}</td>
              <td className="num">{fmt(r.baseline_rate, 2)}</td>
              <td className="num">{r.window_packets}</td>
              <td><button className="btn gold" onClick={async () => {
                await api.patchMeter(r.endpoint_id, { is_mine: 1, is_candidate: 1 }); onLock();
              }}>Lock</button></td>
            </tr>
          ))}
        </tbody>
      </table>

      <h3>Top candidates — spike should align with the shaded window</h3>
      {top.map((r) => (
        <div key={r.endpoint_id} className="panel" style={{ background: "#1e2530" }}>
          <strong>Meter {r.endpoint_id}</strong> <span className="muted">score {fmt(r.score, 1)}×</span>
          <MeterDetail id={r.endpoint_id} hours={hoursSpanning(test.start_ts, data.end_used)}
            bucket="5m" compact windowStart={test.start_ts} windowEnd={data.end_used} />
        </div>
      ))}
    </div>
  );
}

function hoursSpanning(start: string, end: string): number {
  const span = (new Date(end).getTime() - new Date(start).getTime()) / 3600_000;
  return Math.max(6, Math.ceil(span * 3)); // pad so baseline before/after is visible
}

function Combined() {
  const [data, setData] = useState<any>(null);
  useEffect(() => { api.combined().then(setData); }, []);
  if (!data) return <div className="panel"><h2>Best across all tests</h2><span className="muted">loading…</span></div>;
  return (
    <div className="panel">
      <h2>Best candidate across all tests</h2>
      <p className="muted">The meter that wins every test is almost certainly yours ({data.tests.length} closed tests).</p>
      <table>
        <thead><tr><th>meter</th><th className="num">wins</th><th className="num">avg score</th><th className="num">min score</th><th className="num">in</th></tr></thead>
        <tbody>
          {data.ranking.slice(0, 8).map((r: any, i: number) => (
            <tr key={r.endpoint_id} className={i === 0 ? "mine" : ""}>
              <td className={i === 0 ? "spike" : ""}>{r.endpoint_id} <span className="muted">{r.commodity}</span></td>
              <td className="num">{r.wins}/{r.tests_total}</td>
              <td className="num">{fmt(r.avg_score, 1)}×</td>
              <td className="num">{fmt(r.min_score, 1)}×</td>
              <td className="num">{r.tests_present}</td>
            </tr>
          ))}
          {!data.ranking.length && <tr><td colSpan={5} className="muted">run & stop a test to populate</td></tr>}
        </tbody>
      </table>
    </div>
  );
}
