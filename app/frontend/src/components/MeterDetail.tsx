import { useEffect, useState } from "react";
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { Star, Radio, EyeOff, Eye, Terminal, Download, CalendarClock, Copy } from "lucide-react";
import { api, HeatCell, DailyPoint, Benchmark } from "../api";
import { fmt, shortTs, tsMs, since, copyText } from "../util";
import { Heatmap } from "./charts";
import { useChartTheme } from "./chartTheme";
import { Button, Input, Field, Badge, Tabs, Segmented, Skeleton, EmptyState, IconButton, InfoHint, useToast } from "../ui";

type DTab = "timeline" | "heatmap" | "daily";
const tickFmt = (t: number) => shortTs(new Date(t).toISOString()).slice(5, 16);

export default function MeterDetail({ id, hours, onChange }: { id: number; hours: number; onChange?: () => void }) {
  const toast = useToast();
  const ch = useChartTheme();
  const [tab, setTab] = useState<DTab>("timeline");
  const [bucket, setBucket] = useState("1h");
  const [data, setData] = useState<any>(null);
  const [cmd, setCmd] = useState("");

  const load = () => api.meter(id, `?since=${since(hours)}&bucket=${bucket}`).then(setData);
  useEffect(() => { setData(null); load(); /* eslint-disable-next-line */ }, [id, bucket, hours]);
  if (!data) return <Skeleton className="h-64" />;

  const ann = data.annotation || {};
  const points = data.points.map((p: any) => ({ t: tsMs(p.ts), c: p.consumption }));
  const deltas = data.deltas.map((d: any) => ({ t: tsMs(d.bucket), delta: d.delta }));
  const patch = (b: any, msg: string) => api.patchMeter(id, b).then(() => { load(); onChange?.(); toast.show(msg, "good"); }).catch((e) => toast.show(String(e), "bad"));

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <span className="id-pill text-small">#{id}</span>
        {ann.publish ? <Badge tone="gold"><Radio size={11} /> publishing</Badge> : ann.is_mine ? <Badge tone="brand">tracked</Badge> : null}
        <div className="ml-auto"><Tabs tabs={[{ id: "timeline", label: "Timeline" }, { id: "heatmap", label: "Heatmap" }, { id: "daily", label: "Daily" }]} value={tab} onChange={setTab} /></div>
      </div>

      {tab === "timeline" && (
        <div className="space-y-3">
          <div className="flex items-center gap-3">
            <Segmented options={[{ value: "5m", label: "5m" }, { value: "1h", label: "1h" }, { value: "1d", label: "1d" }]} value={bucket} onChange={setBucket} />
            <a className="ml-auto" href={`/api/meters/${id}/export.csv?since=${since(hours)}`}><Button size="sm" variant="ghost" icon={<Download size={14} />}>CSV</Button></a>
          </div>
          <div>
            <div className="label mb-1">Cumulative consumption</div>
            <ResponsiveContainer width="100%" height={170}>
              <AreaChart data={points}>
                <defs><linearGradient id="md-cum" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor={ch.brand} stopOpacity={0.22} /><stop offset="100%" stopColor={ch.brand} stopOpacity={0} /></linearGradient></defs>
                <CartesianGrid {...ch.gridProps} />
                <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={tickFmt} {...ch.axisX} />
                <YAxis domain={["auto", "auto"]} tickFormatter={(v) => fmt(v)} {...ch.axisY} />
                <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={ch.tooltipStyle} />
                <Area dataKey="c" stroke={ch.brand} strokeWidth={2} fill="url(#md-cum)" dot={false} isAnimationActive={false} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
          <div>
            <div className="label mb-1">Per-bucket usage ({bucket})</div>
            <ResponsiveContainer width="100%" height={150}>
              <BarChart data={deltas}>
                <CartesianGrid {...ch.gridProps} />
                <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} scale="time" tickFormatter={tickFmt} {...ch.axisX} />
                <YAxis tickFormatter={(v) => fmt(v)} {...ch.axisY} />
                <Tooltip labelFormatter={(t) => shortTs(new Date(t as number).toISOString())} formatter={(v: any) => fmt(v)} contentStyle={ch.tooltipStyle} />
                <Bar dataKey="delta" fill={ch.gold} radius={[2, 2, 0, 0]} isAnimationActive={false} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        </div>
      )}
      {tab === "heatmap" && <HeatmapTab id={id} />}
      {tab === "daily" && <DailyTab id={id} />}

      <div className="rounded-xl border border-border bg-app/40 p-4">
        <div className="label mb-2">Manage</div>
        <Annotations id={id} ann={ann} onSaved={() => { load(); onChange?.(); toast.show("Saved", "good"); }} />
        <div className="mt-3 flex flex-wrap gap-2">
          <Button icon={<Star size={15} className={ann.is_mine ? "fill-gold text-gold" : ""} />} onClick={() => patch({ is_mine: !ann.is_mine }, ann.is_mine ? "Untracked" : "Tracked")}>{ann.is_mine ? "Untrack" : "Track"}</Button>
          <Button variant="gold" icon={<Radio size={15} />} onClick={() => patch({ publish: !ann.publish, is_mine: true }, ann.publish ? "Stopped publishing" : "Publishing to HA")}>{ann.publish ? "Stop publishing" : "Publish to HA"}</Button>
          <Button variant="ghost" icon={ann.ignored ? <Eye size={15} /> : <EyeOff size={15} />} onClick={() => patch({ ignored: !ann.ignored }, ann.ignored ? "Unignored" : "Ignored")}>{ann.ignored ? "Unignore" : "Ignore"}</Button>
          <Button variant="ghost" icon={<Terminal size={15} />} onClick={() => api.filterCmd(id).then((r) => setCmd(r.command))}>Filter cmd</Button>
        </div>
        {ann.publish && <PublishConfig id={id} ann={ann} onSaved={() => { load(); onChange?.(); toast.show("Publish config applied", "good"); }} />}
        {cmd && (
          <div className="mt-3 flex items-start gap-2 rounded-lg border border-border bg-app px-3 py-2">
            <code className="mono flex-1 break-all text-micro text-good">{cmd}</code>
            <IconButton label="Copy filter command" onClick={() => copyText(cmd).then(() => toast.show("Copied", "good"))}><Copy size={13} /></IconButton>
          </div>
        )}
      </div>
    </div>
  );
}

function HeatmapTab({ id }: { id: number }) {
  const [cells, setCells] = useState<HeatCell[] | null>(null);
  useEffect(() => { api.profile(id, "heatmap", 14).then(setCells); }, [id]);
  if (!cells) return <Skeleton className="h-32" />;
  if (!cells.length) return <EmptyState icon={<CalendarClock size={20} />} title="Not enough history">A usage heatmap appears once this meter has a couple of weeks of readings.</EmptyState>;
  return <div><div className="label mb-2">Average use by hour × day of week (14 days)</div><Heatmap cells={cells} /></div>;
}

function DailyTab({ id }: { id: number }) {
  const ch = useChartTheme();
  const [daily, setDaily] = useState<DailyPoint[] | null>(null);
  const [bench, setBench] = useState<Benchmark | null>(null);
  useEffect(() => { api.profile(id, "daily", 30).then(setDaily); api.benchmark(id, 7).then(setBench); }, [id]);
  if (!daily) return <Skeleton className="h-44" />;
  return (
    <div className="space-y-3">
      {bench && bench.peers > 0 && (
        <div className="grid grid-cols-3 gap-3 text-center">
          <Mini label="Your 7-day use" value={fmt(bench.yours, 1)} tone="text-brand" />
          <Mini label="Neighbour median" value={fmt(bench.median, 1)} />
          <Mini label="Percentile" value={`${fmt(bench.percentile)}%`} tone={bench.percentile > 66 ? "text-gold" : "text-good"} />
        </div>
      )}
      <div className="label mb-1">Daily consumption (30 days)</div>
      <ResponsiveContainer width="100%" height={180}>
        <BarChart data={(daily || []).map((d) => ({ t: d.day, v: d.value }))}>
          <CartesianGrid {...ch.gridProps} />
          <XAxis dataKey="t" tickFormatter={(d) => String(d).slice(5)} {...ch.axisX} />
          <YAxis tickFormatter={(v) => fmt(v)} {...ch.axisY} />
          <Tooltip formatter={(v: any) => fmt(v)} contentStyle={ch.tooltipStyle} />
          <Bar dataKey="v" fill={ch.brand} radius={[2, 2, 0, 0]} isAnimationActive={false} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}

function Mini({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return <div className="rounded-lg border border-border bg-surface px-3 py-2"><div className="label">{label}</div><div className={"mt-1 text-h2 tabular-nums " + (tone || "text-text")}>{value}</div></div>;
}

function Annotations({ id, ann, onSaved }: { id: number; ann: any; onSaved: () => void }) {
  const [label, setLabel] = useState(ann.label || "");
  const [notes, setNotes] = useState(ann.notes || "");
  return (
    <div className="flex flex-wrap items-end gap-2">
      <Field label="label"><Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. my apartment" /></Field>
      <div className="min-w-[180px] flex-1"><Field label="notes"><Input value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="notes" /></Field></div>
      <Button onClick={() => api.patchMeter(id, { label, notes }).then(onSaved)}>Save</Button>
    </div>
  );
}

function PublishConfig({ id, ann, onSaved }: { id: number; ann: any; onSaved: () => void }) {
  const [name, setName] = useState(ann.pub_name || "");
  const [mult, setMult] = useState(String(ann.pub_multiplier ?? 1));
  const [unit, setUnit] = useState(ann.pub_unit || "");
  const reset = () => { setMult("1"); setUnit(""); return api.patchMeter(id, { pub_multiplier: 1, pub_unit: "" }).then(onSaved); };
  return (
    <div className="mt-3 rounded-xl border border-gold/20 bg-gold/[0.04] p-3">
      <div className="mb-2 flex items-center gap-1.5 text-micro font-medium text-secondary">
        publish config
        <InfoHint>The multiplier converts the raw meter counter to energy: <b>published = raw delta × multiplier</b>. Calibrate it on the Identify page, or set a custom value here. It only changes published / Overview / cost / MQTT values.</InfoHint>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <Field label="HA sensor name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="friendly name" /></Field>
        <Field label="multiplier"><Input value={mult} onChange={(e) => setMult(e.target.value)} className="w-28" /></Field>
        <Field label="unit"><Input value={unit} onChange={(e) => setUnit(e.target.value)} className="w-24" placeholder="kWh" /></Field>
        <Button variant="gold" onClick={() => api.patchMeter(id, { pub_name: name, pub_multiplier: parseFloat(mult) || 1, pub_unit: unit }).then(onSaved)} success="Applied">Apply</Button>
        <Button variant="ghost" onClick={reset} success="Reset">Reset</Button>
      </div>
    </div>
  );
}
