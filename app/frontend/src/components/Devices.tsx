import { useEffect, useState } from "react";
import { api, Device, ScanSettings, CoverageCell } from "../api";
import { useLive } from "../App";
import { fmt, shortTs } from "../util";
import { Card, SectionTitle, Stat, Badge, Dot, Button, Input, Field, Toggle, EmptyState, Skeleton, useToast } from "../ui";

const FREQ_PRESETS: Record<string, string> = {
  "912600155": "US 915 MHz ISM (912.6)",
  "868000000": "EU 868 MHz",
};
const MSG_TYPES = ["scm", "scm+", "idm", "r900", "r900bcd"];

export default function Devices() {
  const { tick } = useLive();
  const [data, setData] = useState<{ devices: Device[]; scan: ScanSettings } | null>(null);
  const load = () => api.devices().then(setData);
  useEffect(() => { load(); /* eslint-disable-next-line */ }, [tick]);

  return (
    <div className="space-y-4">
      <ScanCard scan={data?.scan} onSaved={load} />
      <Card>
        <SectionTitle sub="Every detected RTL-SDR dongle. Toggle which are used and set a per-device gain — changes apply live, no restart.">
          SDR devices
        </SectionTitle>
        {!data ? <div className="grid gap-3 sm:grid-cols-2">{[0, 1].map((i) => <Skeleton key={i} className="h-32" />)}</div>
          : data.devices.length === 0 ? <EmptyState>No dongles detected yet. Capture re-scans every 30s.</EmptyState>
            : <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {data.devices.map((d) => <DeviceCard key={d.serial} d={d} onChange={load} />)}
            </div>}
      </Card>
      <Coverage />
    </div>
  );
}

function ScanCard({ scan, onSaved }: { scan?: ScanSettings; onSaved: () => void }) {
  const [s, setS] = useState<ScanSettings | null>(null);
  useEffect(() => { if (scan && !s) setS(scan); }, [scan]); // eslint-disable-line
  if (!s) return <Card><Skeleton className="h-24" /></Card>;
  const types = new Set(s.msgtype.split(",").map((x) => x.trim()).filter(Boolean));
  const toggleType = (t: string) => {
    const n = new Set(types); n.has(t) ? n.delete(t) : n.add(t);
    setS({ ...s, msgtype: [...n].join(",") });
  };
  const apply = () => api.putSettings({
    scan_freq: s.freq, scan_gain: s.gain, scan_ppm: s.ppm, scan_msgtype: s.msgtype, scan_filterid: s.filterid,
  }).then(onSaved);

  return (
    <Card>
      <SectionTitle sub="Tuning shared by all dongles. The capture service hot-restarts its receivers within a few seconds of saving.">
        Scan settings
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
        <div className="flex flex-wrap gap-2">
          {MSG_TYPES.map((t) => (
            <button key={t} onClick={() => toggleType(t)}
              className={"rounded-md border px-2.5 py-1 text-xs transition " +
                (types.has(t) ? "border-brand/40 bg-brand/15 text-brand" : "border-border text-muted hover:text-text")}>
              {t}
            </button>
          ))}
        </div>
      </div>
      <div className="mt-4 flex items-center gap-2">
        <Button variant="primary" success="Scan settings applied — capture reloading" onClick={apply}>Apply scan settings</Button>
        {scan && <span className="text-xs text-muted">{FREQ_PRESETS[s.freq] || "custom band"}</span>}
      </div>
    </Card>
  );
}

function DeviceCard({ d, onChange }: { d: Device; onChange: () => void }) {
  const toast = useToast();
  const [gain, setGain] = useState(d.gain);
  useEffect(() => setGain(d.gain), [d.gain]);
  const save = (body: { enabled?: boolean; gain?: string; label?: string }) =>
    api.putDevice(d.serial, body).then(() => { onChange(); }).catch((e) => toast.show(String(e), "bad"));

  return (
    <div className={"card p-4 " + (d.enabled ? "" : "opacity-60")}>
      <div className="flex items-start justify-between">
        <div>
          <div className="font-medium">{d.label || d.name || "RTL-SDR"}</div>
          <div className="mono text-[11px] text-faint">{d.serial} · dev{d.dev_index}</div>
        </div>
        <Toggle checked={d.enabled} onChange={(v) => save({ enabled: v, gain })} />
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <Badge tone={d.alive ? "good" : "bad"}><Dot ok={d.alive} /> {d.alive ? "live" : "idle"}</Badge>
        <Badge>{d.packets_last_min}/min</Badge>
        <Badge>{d.meters_heard} meters</Badge>
        {d.tuner && <Badge>{d.tuner}</Badge>}
      </div>
      <div className="mt-3 flex items-end gap-2">
        <Field label="Gain override"><Input value={gain} onChange={(e) => setGain(e.target.value)} placeholder="auto" className="w-24" /></Field>
        <Button size="sm" success="Gain applied" onClick={() => save({ enabled: d.enabled, gain })}>Set</Button>
      </div>
      <div className="mt-2 text-[11px] text-faint">last heard {shortTs(d.last_seen)}</div>
    </div>
  );
}

function Coverage() {
  const [cells, setCells] = useState<CoverageCell[] | null>(null);
  useEffect(() => { api.coverage().then(setCells).catch(() => setCells([])); }, []);
  if (!cells) return <Card><Skeleton className="h-24" /></Card>;
  if (!cells.length) return null;

  // build sources × meters matrix
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
