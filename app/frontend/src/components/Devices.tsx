import { useEffect, useState } from "react";
import { api, Device, ScanSettings, DeviceConfig, CoverageCell } from "../api";
import { useLive } from "../App";
import { fmt, shortTs } from "../util";
import { Card, SectionTitle, Stat, Badge, Dot, Button, Input, Field, Toggle, EmptyState, Skeleton, useToast } from "../ui";

const FREQ_PRESETS: Record<string, string> = {
  "912600155": "US 915 MHz ISM (912.6)",
  "868000000": "EU 868 MHz",
};
const MSG_TYPES = ["scm", "scm+", "idm", "r900", "r900bcd"];

function MsgTypeChips({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const set = new Set(value.split(",").map((x) => x.trim()).filter(Boolean));
  const toggle = (t: string) => { const n = new Set(set); n.has(t) ? n.delete(t) : n.add(t); onChange([...n].join(",")); };
  return (
    <div className="flex flex-wrap gap-1.5">
      {MSG_TYPES.map((t) => (
        <button key={t} type="button" onClick={() => toggle(t)}
          className={"rounded-md border px-2 py-0.5 text-xs transition " +
            (set.has(t) ? "border-brand/40 bg-brand/15 text-brand" : "border-border text-muted hover:text-text")}>{t}</button>
      ))}
    </div>
  );
}

export default function Devices() {
  const { tick } = useLive();
  const [data, setData] = useState<{ devices: Device[]; defaults: ScanSettings } | null>(null);
  const load = () => api.devices().then(setData);
  useEffect(() => { load(); /* eslint-disable-next-line */ }, [tick]);

  return (
    <div className="space-y-4">
      <ScanCard defaults={data?.defaults} onSaved={load} />
      <Card>
        <SectionTitle sub="Every detected RTL-SDR dongle. Toggle which are used and override any scan setting per dongle — changes apply live, only the affected dongle restarts.">
          SDR devices
        </SectionTitle>
        {!data ? <div className="grid gap-3 sm:grid-cols-2">{[0, 1].map((i) => <Skeleton key={i} className="h-32" />)}</div>
          : data.devices.length === 0 ? <EmptyState>No dongles detected yet. Capture re-scans on restart.</EmptyState>
            : <div className="grid gap-3 lg:grid-cols-2">
              {data.devices.map((d) => <DeviceCard key={d.serial} d={d} defaults={data.defaults} onChange={load} />)}
            </div>}
      </Card>
      <Coverage />
    </div>
  );
}

function ScanCard({ defaults, onSaved }: { defaults?: ScanSettings; onSaved: () => void }) {
  const [s, setS] = useState<ScanSettings | null>(null);
  useEffect(() => { if (defaults && !s) setS(defaults); }, [defaults]); // eslint-disable-line
  if (!s) return <Card><Skeleton className="h-24" /></Card>;
  const apply = () => api.putSettings({
    scan_freq: s.freq, scan_gain: s.gain, scan_ppm: s.ppm, scan_msgtype: s.msgtype, scan_filterid: s.filterid,
  }).then(onSaved);

  return (
    <Card>
      <SectionTitle sub="The baseline every dongle inherits unless it overrides a field below. Capture hot-restarts affected receivers within a few seconds of saving.">
        Default scan settings
      </SectionTitle>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Field label="Center frequency (Hz)">
          <Input value={s.freq} onChange={(e) => setS({ ...s, freq: e.target.value })} list="freqs" />
          <datalist id="freqs">{Object.keys(FREQ_PRESETS).map((f) => <option key={f} value={f}>{FREQ_PRESETS[f]}</option>)}</datalist>
        </Field>
        <Field label="Gain (blank = auto)"><Input value={s.gain} onChange={(e) => setS({ ...s, gain: e.target.value })} placeholder="auto" /></Field>
        <Field label="PPM correction"><Input value={s.ppm} onChange={(e) => setS({ ...s, ppm: e.target.value })} placeholder="0" /></Field>
        <Field label="Filter to meter id (optional)"><Input value={s.filterid} onChange={(e) => setS({ ...s, filterid: e.target.value })} placeholder="all meters" /></Field>
      </div>
      <div className="mt-3">
        <div className="label mb-1.5">Message types</div>
        <MsgTypeChips value={s.msgtype} onChange={(v) => setS({ ...s, msgtype: v })} />
      </div>
      <div className="mt-4 flex items-center gap-2">
        <Button variant="primary" success="Defaults applied — capture reloading" onClick={apply}>Apply defaults</Button>
        <span className="text-xs text-muted">{FREQ_PRESETS[s.freq] || "custom band"}</span>
      </div>
    </Card>
  );
}

function DeviceCard({ d, defaults, onChange }: { d: Device; defaults: ScanSettings; onChange: () => void }) {
  const toast = useToast();
  const init = (): DeviceConfig => ({ enabled: d.enabled, label: d.label, freq: d.freq, gain: d.gain, ppm: d.ppm, msgtype: d.msgtype, filterid: d.filterid });
  const [cfg, setCfg] = useState<DeviceConfig>(init);
  const [open, setOpen] = useState(false);
  useEffect(() => setCfg(init()), [d.serial, d.enabled, d.freq, d.gain, d.ppm, d.msgtype, d.filterid, d.label]); // eslint-disable-line

  const save = (next: DeviceConfig) => api.putDevice(d.serial, next).then(() => onChange()).catch((e) => toast.show(String(e), "bad"));
  const overridden = !!(d.freq || d.gain || d.ppm || d.msgtype || d.filterid);

  return (
    <div className={"card p-4 " + (cfg.enabled ? "" : "opacity-60")}>
      <div className="flex items-start justify-between">
        <div>
          <div className="font-medium">{d.label || d.name || "RTL-SDR"}</div>
          <div className="mono text-[11px] text-faint">{d.serial} · dev{d.dev_index}{d.tuner ? ` · ${d.tuner}` : ""}</div>
        </div>
        <Toggle checked={!!cfg.enabled} onChange={(v) => { setCfg({ ...cfg, enabled: v }); save({ ...cfg, enabled: v }); }} />
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <Badge tone={d.alive ? "good" : "bad"}><Dot ok={d.alive} /> {d.alive ? "live" : "idle"}</Badge>
        <Badge>{d.packets_last_min}/min</Badge>
        <Badge>{d.meters_heard} meters</Badge>
        {overridden && <Badge tone="info">custom scan</Badge>}
        <span className="ml-auto text-[11px] text-faint">last heard {shortTs(d.last_seen)}</span>
      </div>

      <button onClick={() => setOpen(!open)} className="mt-3 text-xs text-brand hover:underline">{open ? "▾ hide scan settings" : "▸ scan settings"}</button>
      {open && (
        <div className="mt-2 space-y-3 rounded-lg border border-border bg-bg/30 p-3">
          <p className="text-xs text-muted">Blank = inherit the default. Set any field to override it just for this dongle.</p>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Frequency (Hz)"><Input value={cfg.freq || ""} onChange={(e) => setCfg({ ...cfg, freq: e.target.value })} placeholder={defaults.freq} list="freqs" /></Field>
            <Field label="Gain"><Input value={cfg.gain || ""} onChange={(e) => setCfg({ ...cfg, gain: e.target.value })} placeholder={defaults.gain || "auto"} /></Field>
            <Field label="PPM"><Input value={cfg.ppm || ""} onChange={(e) => setCfg({ ...cfg, ppm: e.target.value })} placeholder={defaults.ppm || "0"} /></Field>
            <Field label="Filter id"><Input value={cfg.filterid || ""} onChange={(e) => setCfg({ ...cfg, filterid: e.target.value })} placeholder={defaults.filterid || "all"} /></Field>
          </div>
          <div>
            <div className="label mb-1">Message types <span className="text-faint normal-case">(default: {defaults.msgtype})</span></div>
            <MsgTypeChips value={cfg.msgtype || defaults.msgtype} onChange={(v) => setCfg({ ...cfg, msgtype: v })} />
          </div>
          <Field label="Label"><Input value={cfg.label || ""} onChange={(e) => setCfg({ ...cfg, label: e.target.value })} placeholder="friendly name" /></Field>
          <div className="flex gap-2">
            <Button size="sm" variant="primary" success="Applied — dongle restarting" onClick={() => save(cfg)}>Apply to this dongle</Button>
            <Button size="sm" variant="ghost" success="Reset to defaults" onClick={() => save({ enabled: cfg.enabled, label: cfg.label, freq: "", gain: "", ppm: "", msgtype: "", filterid: "" })}>Use defaults</Button>
          </div>
        </div>
      )}
    </div>
  );
}

function Coverage() {
  const [cells, setCells] = useState<CoverageCell[] | null>(null);
  useEffect(() => { api.coverage().then(setCells).catch(() => setCells([])); }, []);
  if (!cells) return <Card><Skeleton className="h-24" /></Card>;
  if (!cells.length) return null;

  const sources = [...new Set(cells.map((c) => c.source))].sort();
  const meters = [...new Set(cells.map((c) => c.endpoint_id))];
  const get: Record<string, number> = {};
  let max = 1;
  cells.forEach((c) => { get[`${c.source}-${c.endpoint_id}`] = c.packets; max = Math.max(max, c.packets); });
  const perSource = sources.map((s) => ({ s, total: cells.filter((c) => c.source === s).reduce((a, c) => a + c.packets, 0), heard: new Set(cells.filter((c) => c.source === s).map((c) => c.endpoint_id)).size }));

  return (
    <Card>
      <SectionTitle sub="Which dongle hears which meter (all-time packet counts). Use it to place/aim your SDRs for the best coverage.">
        Reception coverage
      </SectionTitle>
      <div className="mb-3 grid grid-cols-2 gap-3 sm:grid-cols-3">
        {perSource.map((p) => <Stat key={p.s} label={<span className="mono">{p.s}</span>} value={p.heard} sub={`${fmt(p.total)} pkts · meters heard`} />)}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-xs">
          <thead><tr className="text-left text-faint">
            <th className="py-1 pr-3 font-medium">meter</th>
            {sources.map((s) => <th key={s} className="px-2 py-1 text-center font-medium mono">{s}</th>)}
          </tr></thead>
          <tbody>
            {meters.slice(0, 60).map((m) => (
              <tr key={m} className="border-t border-border/50">
                <td className="py-1 pr-3 mono text-muted">#{m}</td>
                {sources.map((s) => {
                  const v = get[`${s}-${m}`] || 0;
                  return <td key={s} className="px-1 py-1 text-center">
                    <span className="inline-block w-full rounded px-1 tabular-nums"
                      style={{ background: v ? `rgba(45,212,191,${0.1 + (v / max) * 0.7})` : "transparent", color: v ? "#04241f" : "#3b4654" }}>
                      {v || "·"}
                    </span>
                  </td>;
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}
