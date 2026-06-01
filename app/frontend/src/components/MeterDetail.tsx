import { useEffect, useState } from "react";
import {
  Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis,
} from "recharts";
import { api, HeatCell, DailyPoint, Benchmark } from "../api";
import { fmt, shortTs, tsMs, since } from "../util";
import { Heatmap } from "./charts";
import { Button, Input, Field, Badge, Tabs, Stat, useToast, Spinner } from "../ui";

const GRID = "#1e2935", AXIS = "#6b7a8d";
const tip = { background: "#1d2937", border: "1px solid #26323f", borderRadius: 8, fontSize: 12 };
type DTab = "timeline" | "heatmap" | "daily";

export default function MeterDetail({ id, hours, onChange }: { id: number; hours: number; onChange?: () => void }) {
  const toast = useToast();
  const [tab, setTab] = useState<DTab>("timeline");
  const [bucket, setBucket] = useState("1h");
  const [data, setData] = useState<any>(null);
  const [cmd, setCmd] = useState("");

  const load = () => api.meter(id, `?since=${since(hours)}&bucket=${bucket}`).then(setData);
  useEffect(() => { load(); /* eslint-disable-next-line */ }, [id, bucket, hours]);
  if (!data) return <div className="flex items-center gap-2 text-muted"><Spinner /> loading meter #{id}…</div>;

  const ann = data.annotation || {};
  const points = data.points.map((p: any) => ({ t: tsMs(p.ts), c: p.consumption }));
  const deltas = data.deltas.map((d: any) => ({ t: tsMs(d.bucket), delta: d.delta }));
  const tickFmt = (t: number) => shortTs(new Date(t).toISOString()).slice(5, 16);
  const patch = (b: any, msg: string) => api.patchMeter(id, b).then(() => { load(); onChange?.(); toast.show(msg, "good"); }).catch((e) => toast.show(String(e), "bad"));

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-[15px]">Meter <span className="mono text-brand">#{id}</span></h2>
        {ann.publish ? <Badge tone="gold">▲ publishing</Badge> : ann.is_mine ? <Badge tone="brand">tracked</Badge> : null}
        <div className="ml-auto"><Tabs tabs={[{ id: "timeline", label: "Timeline" }, { id: "heatmap", label: "Heatmap" }, { id: "daily", label: "Daily" }]} value={tab} onChange={setTab} /></div>
      </div>

      {tab === "timeline" && (
        <div className="space-y-3">
          <div className="flex items-center gap-2">
            <span className="label">bucket</span>
            {["5m", "1h", "1d"].map((b) => (
              <button key={b} onClick={() => setBucket(b)} className={"rounded-md border px-2 py-0.5 text-xs " + (bucket === b ? "border-brand/40 bg-brand/15 text-brand" : "border-border text-muted")}>{b}</button>
            ))}
            <a className="ml-auto" href={`/api/meters/${id}/export.csv?since=${since(hours)}`}><Button size="sm" variant="ghost">Export CSV</Button></a>
          </div>
          <div>
            <div className="label mb-1">Cumulative consumption</div>
            <ResponsiveContainer width="100%" height={170}>
              <AreaChart data={points}>
                <CartesianGrid stroke={GRID} />
                <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={tickFmt} stroke={AXIS} fontSize={11} />
                <YAxis domain={["auto", "auto"]} stroke={AXIS} fontSize={11} tickFormatter={(v) => fmt(v)} width={70} />
                <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={tip} />
                <Area dataKey="c" stroke="#2dd4bf" fill="#2dd4bf" fillOpacity={0.14} dot={false} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
          <div>
            <div className="label mb-1">Per-bucket usage ({bucket})</div>
            <ResponsiveContainer width="100%" height={150}>
              <BarChart data={deltas}>
                <CartesianGrid stroke={GRID} />
                <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={tickFmt} stroke={AXIS} fontSize={11} />
                <YAxis stroke={AXIS} fontSize={11} tickFormatter={(v) => fmt(v)} width={70} />
                <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={tip} />
                <Bar dataKey="delta" fill="#fbbf24" radius={[2, 2, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        </div>
      )}

      {tab === "heatmap" && <HeatmapTab id={id} />}
      {tab === "daily" && <DailyTab id={id} />}

      <div className="rounded-lg border border-border bg-surface2/40 p-4">
        <div className="label mb-2">Manage</div>
        <Annotations id={id} ann={ann} onSaved={() => { load(); onChange?.(); toast.show("Saved", "good"); }} />
        <div className="mt-3 flex flex-wrap gap-2">
          <Button onClick={() => patch({ is_mine: !ann.is_mine }, ann.is_mine ? "Untracked" : "Tracked")}>{ann.is_mine ? "Untrack" : "Track as mine"}</Button>
          <Button variant="gold" onClick={() => patch({ publish: !ann.publish, is_mine: true }, ann.publish ? "Stopped publishing" : "Publishing to HA")}>{ann.publish ? "Stop publishing" : "Publish to HA"}</Button>
          <Button variant="ghost" onClick={() => patch({ ignored: !ann.ignored }, ann.ignored ? "Unignored" : "Ignored")}>{ann.ignored ? "Unignore" : "Ignore"}</Button>
          <Button variant="ghost" onClick={() => api.filterCmd(id).then((r) => setCmd(r.command))}>Show filter cmd</Button>
        </div>
        {ann.publish && <PublishConfig id={id} ann={ann} onSaved={() => { load(); onChange?.(); toast.show("Publish config applied", "good"); }} />}
        {cmd && <div className="mono mt-3 rounded-md border border-border bg-bg px-3 py-2 text-xs text-good">{cmd}</div>}
      </div>
    </div>
  );
}

function HeatmapTab({ id }: { id: number }) {
  const [cells, setCells] = useState<HeatCell[] | null>(null);
  useEffect(() => { api.profile(id, "heatmap", 14).then(setCells); }, [id]);
  if (!cells) return <div className="text-muted">loading…</div>;
  if (!cells.length) return <div className="text-faint italic">Not enough history yet.</div>;
  return (
    <div>
      <div className="label mb-2">Average use by hour × day of week (last 14 days)</div>
      <Heatmap cells={cells} />
    </div>
  );
}

function DailyTab({ id }: { id: number }) {
  const [daily, setDaily] = useState<DailyPoint[] | null>(null);
  const [bench, setBench] = useState<Benchmark | null>(null);
  useEffect(() => { api.profile(id, "daily", 30).then(setDaily); api.benchmark(id, 7).then(setBench); }, [id]);
  return (
    <div className="space-y-3">
      {bench && bench.peers > 0 && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
          <Stat label="Your 7-day use" tone="brand" value={fmt(bench.yours, 1)} sub={bench.commodity} />
          <Stat label="Neighbour median" value={fmt(bench.median, 1)} sub={`${bench.peers} peers`} />
          <Stat label="Percentile" tone={bench.percentile > 66 ? "gold" : "good"} value={`${fmt(bench.percentile)}%`} sub="vs same-commodity meters" />
        </div>
      )}
      <div className="label mb-1">Daily consumption (30 days)</div>
      <ResponsiveContainer width="100%" height={180}>
        <BarChart data={(daily || []).map((d) => ({ t: d.day, v: d.value }))}>
          <CartesianGrid stroke={GRID} />
          <XAxis dataKey="t" stroke={AXIS} fontSize={11} tickFormatter={(d) => String(d).slice(5)} />
          <YAxis stroke={AXIS} fontSize={11} tickFormatter={(v) => fmt(v)} width={60} />
          <Tooltip formatter={(v: any) => fmt(v)} contentStyle={tip} />
          <Bar dataKey="v" fill="#2dd4bf" radius={[2, 2, 0, 0]} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}

function Annotations({ id, ann, onSaved }: { id: number; ann: any; onSaved: () => void }) {
  const [label, setLabel] = useState(ann.label || "");
  const [notes, setNotes] = useState(ann.notes || "");
  return (
    <div className="flex flex-wrap items-end gap-2">
      <Field label="label"><Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. my apartment" /></Field>
      <div className="flex-1 min-w-[180px]"><Field label="notes"><Input value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="notes" /></Field></div>
      <Button onClick={() => api.patchMeter(id, { label, notes }).then(onSaved)}>Save</Button>
    </div>
  );
}

function PublishConfig({ id, ann, onSaved }: { id: number; ann: any; onSaved: () => void }) {
  const [name, setName] = useState(ann.pub_name || "");
  const [mult, setMult] = useState(String(ann.pub_multiplier ?? 1));
  const [unit, setUnit] = useState(ann.pub_unit || "");
  return (
    <div className="mt-3 flex flex-wrap items-end gap-2 rounded-lg border border-gold/20 bg-gold/5 p-3">
      <Field label="HA sensor name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="friendly name" /></Field>
      <Field label="multiplier"><Input value={mult} onChange={(e) => setMult(e.target.value)} className="w-28" /></Field>
      <Field label="unit"><Input value={unit} onChange={(e) => setUnit(e.target.value)} className="w-24" placeholder="kWh" /></Field>
      <Button variant="gold" onClick={() => api.patchMeter(id, { pub_name: name, pub_multiplier: parseFloat(mult) || 1, pub_unit: unit }).then(onSaved)}>Apply</Button>
    </div>
  );
}
