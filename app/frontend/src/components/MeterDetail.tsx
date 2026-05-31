import { useEffect, useState } from "react";
import {
  Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis,
} from "recharts";
import { api } from "../api";
import { fmt, shortTs, tsMs, since } from "../util";

const BUCKETS = ["5m", "1h", "1d"];

export default function MeterDetail({ id, hours, onChange }:
  { id: number; hours: number; onChange?: () => void }) {
  const [bucket, setBucket] = useState("1h");
  const [data, setData] = useState<any>(null);
  const [cmd, setCmd] = useState("");

  const load = () => api.meter(id, `?since=${since(hours)}&bucket=${bucket}`).then(setData);
  useEffect(() => { load(); /* eslint-disable-next-line */ }, [id, bucket, hours]);
  if (!data) return <div className="muted">loading meter {id}…</div>;

  const ann = data.annotation || {};
  const points = data.points.map((p: any) => ({ t: tsMs(p.ts), c: p.consumption }));
  const deltas = data.deltas.map((d: any) => ({ t: tsMs(d.bucket), delta: d.delta }));
  const tickFmt = (t: number) => shortTs(new Date(t).toISOString()).slice(5, 16);
  const patch = (b: any) => api.patchMeter(id, b).then(() => { load(); onChange?.(); });

  return (
    <div>
      <div className="controls">
        <strong>Meter {id}</strong>
        {ann.publish ? <span className="chip electric">▲ publishing</span> : ann.is_mine ? <span className="chip">tracked</span> : null}
        <label>bucket</label>
        <select value={bucket} onChange={(e) => setBucket(e.target.value)}>{BUCKETS.map((b) => <option key={b}>{b}</option>)}</select>
        <a className="btn alt" href={`/api/meters/${id}/export.csv?since=${since(hours)}`} style={{ textDecoration: "none" }}>Export CSV</a>
      </div>

      <h3>Cumulative consumption</h3>
      <ResponsiveContainer width="100%" height={180}>
        <AreaChart data={points}>
          <CartesianGrid stroke="#2a3340" />
          <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={tickFmt} stroke="#8a94a6" fontSize={11} />
          <YAxis domain={["auto", "auto"]} stroke="#8a94a6" fontSize={11} tickFormatter={(v) => fmt(v)} width={70} />
          <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={{ background: "#171c24", border: "1px solid #2a3340" }} />
          <Area dataKey="c" stroke="#4db6ac" fill="#4db6ac" fillOpacity={0.15} dot={false} />
        </AreaChart>
      </ResponsiveContainer>

      <h3>Per-bucket usage ({bucket})</h3>
      <ResponsiveContainer width="100%" height={160}>
        <BarChart data={deltas}>
          <CartesianGrid stroke="#2a3340" />
          <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={tickFmt} stroke="#8a94a6" fontSize={11} />
          <YAxis stroke="#8a94a6" fontSize={11} tickFormatter={(v) => fmt(v)} width={70} />
          <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={{ background: "#171c24", border: "1px solid #2a3340" }} />
          <Bar dataKey="delta" fill="#ffca28" />
        </BarChart>
      </ResponsiveContainer>

      <div className="panel" style={{ background: "#1e2530", marginTop: 14 }}>
        <h3>Manage</h3>
        <Annotations id={id} ann={ann} onSaved={() => { load(); onChange?.(); }} />
        <div style={{ marginTop: 10 }}>
          <button className="btn alt" onClick={() => patch({ is_mine: !ann.is_mine })}>{ann.is_mine ? "Untrack" : "Track as mine"}</button>{" "}
          <button className="btn gold" onClick={() => patch({ publish: !ann.publish, is_mine: true })}>{ann.publish ? "Stop publishing" : "Publish to HA"}</button>{" "}
          <button className="btn alt" onClick={() => patch({ ignored: !ann.ignored })}>{ann.ignored ? "Unignore" : "Ignore"}</button>{" "}
          <button className="btn alt" onClick={() => api.filterCmd(id).then((r) => setCmd(r.command))}>Show filter cmd</button>
        </div>
        {ann.publish && <PublishConfig id={id} ann={ann} onSaved={() => { load(); onChange?.(); }} />}
        {cmd && <div className="cmd" style={{ marginTop: 10 }}>{cmd}</div>}
      </div>
    </div>
  );
}

function Annotations({ id, ann, onSaved }: { id: number; ann: any; onSaved: () => void }) {
  const [label, setLabel] = useState(ann.label || "");
  const [notes, setNotes] = useState(ann.notes || "");
  return (
    <div className="controls">
      <label>label</label>
      <input type="text" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. my apartment" />
      <input type="text" value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="notes" style={{ flex: 1 }} />
      <button className="btn alt" onClick={() => api.patchMeter(id, { label, notes }).then(onSaved)}>Save</button>
    </div>
  );
}

function PublishConfig({ id, ann, onSaved }: { id: number; ann: any; onSaved: () => void }) {
  const [name, setName] = useState(ann.pub_name || "");
  const [mult, setMult] = useState(String(ann.pub_multiplier ?? 1));
  const [unit, setUnit] = useState(ann.pub_unit || "");
  return (
    <div className="controls" style={{ marginTop: 10 }}>
      <span className="muted">HA sensor:</span>
      <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="friendly name" />
      <input type="text" value={mult} onChange={(e) => setMult(e.target.value)} placeholder="multiplier" style={{ width: 90 }} />
      <input type="text" value={unit} onChange={(e) => setUnit(e.target.value)} placeholder="unit (kWh)" style={{ width: 90 }} />
      <button className="btn alt" onClick={() =>
        api.patchMeter(id, { pub_name: name, pub_multiplier: parseFloat(mult) || 1, pub_unit: unit }).then(onSaved)}>Apply</button>
    </div>
  );
}
