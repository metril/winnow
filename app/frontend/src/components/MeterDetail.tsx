import { useEffect, useState } from "react";
import {
  Area, AreaChart, Bar, BarChart, CartesianGrid, ReferenceArea,
  ResponsiveContainer, Tooltip, XAxis, YAxis,
} from "recharts";
import { api } from "../api";
import { fmt, shortTs, tsMs } from "../util";

interface Props {
  id: number;
  hours: number;
  bucket?: string;
  // optional shaded load-test window, as ISO strings
  windowStart?: string | null;
  windowEnd?: string | null;
  compact?: boolean;
}

const BUCKETS = ["5m", "1h", "1d"];

export default function MeterDetail({
  id, hours, bucket: initialBucket = "1h", windowStart, windowEnd, compact,
}: Props) {
  const [bucket, setBucket] = useState(initialBucket);
  const [data, setData] = useState<any>(null);
  const [cmd, setCmd] = useState<string>("");
  const [copied, setCopied] = useState(false);

  const sinceIso = new Date(Date.now() - hours * 3600_000).toISOString();

  const load = () =>
    api.meter(id, `?since=${sinceIso}&bucket=${bucket}`).then(setData);

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, bucket, hours]);

  if (!data) return <div className="muted">loading meter {id}…</div>;

  const ann = data.annotation || {};
  const points = data.points.map((p: any) => ({ t: tsMs(p.ts), c: p.consumption }));
  const deltas = data.deltas.map((d: any) => ({
    t: tsMs(d.bucket.length <= 10 ? d.bucket + "T00:00:00Z" : d.bucket + ":00Z"),
    delta: d.delta,
  }));
  const winLo = windowStart ? tsMs(windowStart) : null;
  const winHi = windowEnd ? tsMs(windowEnd) : null;

  const lockMine = async () => {
    await api.patchMeter(id, { is_mine: 1, is_candidate: 1 });
    const r = await api.filterCmd(id);
    setCmd(r.command);
    load();
  };

  const csvUrl = `/api/meters/${id}/export.csv?since=${sinceIso}`;
  const tickFmt = (t: number) => shortTs(new Date(t).toISOString()).slice(5, 16);

  return (
    <div>
      <div className="controls">
        <strong>Meter {id}</strong>
        {ann.is_mine ? <span className="chip electric">MINE</span> : null}
        {!compact && (
          <>
            <label>bucket</label>
            <select value={bucket} onChange={(e) => setBucket(e.target.value)}>
              {BUCKETS.map((b) => <option key={b}>{b}</option>)}
            </select>
            <a className="btn alt" href={csvUrl} style={{ textDecoration: "none" }}>
              Export CSV
            </a>
          </>
        )}
      </div>

      <h3>Cumulative consumption (odometer)</h3>
      <ResponsiveContainer width="100%" height={compact ? 140 : 200}>
        <AreaChart data={points}>
          <CartesianGrid stroke="#2a3340" />
          <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]}
            tickFormatter={tickFmt} stroke="#8a94a6" fontSize={11} />
          <YAxis domain={["auto", "auto"]} stroke="#8a94a6" fontSize={11}
            tickFormatter={(v) => fmt(v)} width={70} />
          <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())}
            formatter={(v: any) => fmt(v)} contentStyle={{ background: "#171c24", border: "1px solid #2a3340" }} />
          {winLo && winHi && <ReferenceArea x1={winLo} x2={winHi} fill="#ffca28" fillOpacity={0.15} />}
          <Area dataKey="c" stroke="#4db6ac" fill="#4db6ac" fillOpacity={0.15} dot={false} />
        </AreaChart>
      </ResponsiveContainer>

      <h3>Per-bucket usage delta ({bucket})</h3>
      <ResponsiveContainer width="100%" height={compact ? 140 : 200}>
        <BarChart data={deltas}>
          <CartesianGrid stroke="#2a3340" />
          <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]}
            tickFormatter={tickFmt} stroke="#8a94a6" fontSize={11} />
          <YAxis stroke="#8a94a6" fontSize={11} tickFormatter={(v) => fmt(v)} width={70} />
          <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())}
            formatter={(v: any) => fmt(v)} contentStyle={{ background: "#171c24", border: "1px solid #2a3340" }} />
          {winLo && winHi && <ReferenceArea x1={winLo} x2={winHi} fill="#ffca28" fillOpacity={0.18} />}
          <Bar dataKey="delta" fill="#ffca28" />
        </BarChart>
      </ResponsiveContainer>

      {!compact && (
        <div className="panel" style={{ marginTop: 16 }}>
          <h3>Candidate management</h3>
          <Annotations id={id} ann={ann} onChange={load} />
          <div style={{ marginTop: 12 }}>
            <button className="btn gold" onClick={lockMine}>🔒 Lock as mine</button>{" "}
            <button className="btn alt" onClick={() => api.patchMeter(id, { is_candidate: 1 }).then(load)}>
              ⭐ Flag candidate
            </button>
          </div>
          {(cmd || ann.is_mine) && (
            <FilterCommand id={id} initial={cmd} onCopy={() => { setCopied(true); setTimeout(() => setCopied(false), 1500); }} copied={copied} />
          )}
        </div>
      )}
    </div>
  );
}

function Annotations({ id, ann, onChange }: { id: number; ann: any; onChange: () => void }) {
  const [label, setLabel] = useState(ann.label || "");
  const [notes, setNotes] = useState(ann.notes || "");
  return (
    <div>
      <div className="controls">
        <label>label</label>
        <input type="text" value={label} onChange={(e) => setLabel(e.target.value)}
          placeholder="e.g. maybe mine?" />
        <button className="btn alt" onClick={() => api.patchMeter(id, { label, notes }).then(onChange)}>
          Save
        </button>
      </div>
      <textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)}
        placeholder="notes…" />
    </div>
  );
}

function FilterCommand({ id, initial, onCopy, copied }:
  { id: number; initial: string; onCopy: () => void; copied: boolean }) {
  const [cmd, setCmd] = useState(initial);
  useEffect(() => {
    if (!initial) api.filterCmd(id).then((r) => setCmd(r.command));
    else setCmd(initial);
  }, [id, initial]);
  if (!cmd) return null;
  return (
    <div style={{ marginTop: 14 }}>
      <h3>Downstream pipeline command</h3>
      <div className="cmd">{cmd}</div>
      <button className="btn alt" style={{ marginTop: 8 }}
        onClick={() => { navigator.clipboard?.writeText(cmd); onCopy(); }}>
        {copied ? "Copied!" : "Copy command"}
      </button>
    </div>
  );
}
