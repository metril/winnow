import { useEffect, useState } from "react";
import { RadioTower, Sliders, RotateCcw, Pencil, SatelliteDish, Save } from "lucide-react";
import { api, Device, ScanSettings, DeviceConfig, CoverageCell } from "../api";
import { useLive, perMin } from "../live";
import { useFetch } from "../fetch";
import { useChartTheme } from "./chartTheme";
import { fmt, shortTs } from "../util";
import { Page } from "./shell";
import { Card, CardHeader, CardBody, Button, Input, Field, Badge, Dot, Toggle, EmptyState, Skeleton, useToast } from "../ui";

const FREQ_PRESETS: Record<string, string> = { "912600155": "US 915 MHz ISM (912.6)", "868000000": "EU 868 MHz" };
const MSG_TYPES = ["scm", "scm+", "idm", "r900", "r900bcd"];
const SCAN_FIELDS: (keyof ScanSettings)[] = ["freq", "gain", "ppm", "msgtype", "filterid"];

function MsgTypeChips({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const set = new Set(value.split(",").map((x) => x.trim()).filter(Boolean));
  const toggle = (t: string) => { const n = new Set(set); n.has(t) ? n.delete(t) : n.add(t); onChange([...n].join(",")); };
  return (
    <div className="flex flex-wrap gap-1.5">
      {MSG_TYPES.map((t) => (
        <button key={t} type="button" onClick={() => toggle(t)}
          className={"rounded-md border px-2 py-0.5 text-micro transition " + (set.has(t) ? "border-brand/40 bg-brand/15 text-brand" : "border-border text-tertiary hover:text-secondary")}>{t}</button>
      ))}
    </div>
  );
}

export default function Devices() {
  const { configVersion, readings } = useLive();
  const { data, reload } = useFetch(api.devices, [configVersion]);
  return (
    <Page title="Devices" breadcrumb="Inventory">
      <ScanCard defaults={data?.defaults} onSaved={reload} />
      <Card>
        <CardHeader title="SDR devices" icon={<RadioTower size={16} />}
          subtitle="Each detected dongle. Toggle which are used, and override any scan setting per dongle — only the affected dongle restarts." />
        <CardBody>
          {!data ? <div className="grid gap-3 lg:grid-cols-2">{[0, 1].map((i) => <Skeleton key={i} className="h-40" />)}</div>
            : data.devices.length === 0 ? <EmptyState icon={<SatelliteDish size={22} />} title="No dongles detected">Capture re-scans on restart.</EmptyState>
              : <div className="grid gap-3 lg:grid-cols-2">{data.devices.map((d) => <DeviceCard key={d.serial} d={d} defaults={data.defaults} liveRate={perMin(readings, d.serial)} onChange={reload} />)}</div>}
        </CardBody>
      </Card>
      <Coverage />
    </Page>
  );
}

function ScanCard({ defaults, onSaved }: { defaults?: ScanSettings; onSaved: () => void }) {
  const [s, setS] = useState<ScanSettings | null>(null);
  useEffect(() => { if (defaults && !s) setS(defaults); }, [defaults]); // eslint-disable-line
  if (!s) return <Card><CardBody><Skeleton className="h-24" /></CardBody></Card>;
  const apply = () => api.putSettings({ scan_freq: s.freq, scan_gain: s.gain, scan_ppm: s.ppm, scan_msgtype: s.msgtype, scan_filterid: s.filterid }).then(onSaved);
  return (
    <Card>
      <CardHeader title="Capture defaults" icon={<Sliders size={16} />}
        subtitle="The baseline every dongle inherits. Changing a default applies to every dongle that hasn't overridden that field." />
      <CardBody>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Field label="Center frequency (Hz)"><Input value={s.freq} onChange={(e) => setS({ ...s, freq: e.target.value })} list="freqs" /><datalist id="freqs">{Object.keys(FREQ_PRESETS).map((f) => <option key={f} value={f}>{FREQ_PRESETS[f]}</option>)}</datalist></Field>
          <Field label="Gain (blank = auto)"><Input value={s.gain} onChange={(e) => setS({ ...s, gain: e.target.value })} placeholder="auto" /></Field>
          <Field label="PPM correction"><Input value={s.ppm} onChange={(e) => setS({ ...s, ppm: e.target.value })} placeholder="0" /></Field>
          <Field label="Filter to meter id"><Input value={s.filterid} onChange={(e) => setS({ ...s, filterid: e.target.value })} placeholder="all meters" /></Field>
        </div>
        <div className="mt-3"><div className="label mb-1.5">Message types</div><MsgTypeChips value={s.msgtype} onChange={(v) => setS({ ...s, msgtype: v })} /></div>
        <div className="mt-4 flex items-center gap-2">
          <Button variant="primary" onClick={apply} success="Defaults applied — capture reloading">Apply defaults</Button>
          <span className="text-micro text-tertiary">{FREQ_PRESETS[s.freq] || "custom band"}</span>
        </div>
      </CardBody>
    </Card>
  );
}

const fieldLabel: Record<keyof ScanSettings, string> = {
  freq: "Frequency (Hz)", gain: "Gain", ppm: "PPM", msgtype: "Message types", filterid: "Filter to meter id",
};
const defAuto: Partial<Record<keyof ScanSettings, string>> = { gain: "auto", filterid: "all meters", ppm: "0" };

// OverrideField shows one scan setting as either Inherited (read-only, with the
// resolved default shown explicitly) or Custom (editable). No placeholder ever
// stands in for the inherited value — the state is always an explicit chip.
function OverrideField({ field, value, def, onChange }:
  { field: keyof ScanSettings; value: string; def: string; onChange: (v: string) => void }) {
  const [editing, setEditing] = useState(value !== "");
  const custom = value !== "" || editing;
  const shownDef = def || defAuto[field] || "—";
  const activate = () => { setEditing(true); if (def) onChange(def); };
  const reset = () => { setEditing(false); onChange(""); };
  return (
    <div className="rounded-md border border-border bg-app/40 p-2.5">
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <span className="label">{fieldLabel[field]}</span>
        {custom
          ? <button type="button" onClick={reset} className="inline-flex items-center gap-1 text-micro text-brand hover:underline"><RotateCcw size={11} /> use default</button>
          : <button type="button" onClick={activate} className="inline-flex items-center gap-1 text-micro text-tertiary hover:text-secondary"><Pencil size={11} /> override</button>}
      </div>
      {custom ? (
        field === "msgtype"
          ? <MsgTypeChips value={value || def} onChange={onChange} />
          : <Input value={value} autoFocus onChange={(e) => onChange(e.target.value)} list={field === "freq" ? "freqs" : undefined} placeholder={shownDef} />
      ) : (
        <div className="flex items-center gap-2">
          <Badge>Inherited</Badge>
          <span className="mono truncate text-small text-secondary">{shownDef}</span>
        </div>
      )}
    </div>
  );
}

function DeviceCard({ d, defaults, liveRate, onChange }: { d: Device; defaults: ScanSettings; liveRate: number; onChange: () => void }) {
  const toast = useToast();
  const initScan = (): ScanSettings => ({ freq: d.freq, gain: d.gain, ppm: d.ppm, msgtype: d.msgtype, filterid: d.filterid });
  const [enabled, setEnabled] = useState(!!d.enabled);
  const [label, setLabel] = useState(d.label || "");
  const [scan, setScan] = useState<ScanSettings>(initScan);
  // resync when the underlying device/config changes
  useEffect(() => { setEnabled(!!d.enabled); setLabel(d.label || ""); setScan(initScan()); }, [d.serial, d.enabled, d.freq, d.gain, d.ppm, d.msgtype, d.filterid, d.label]); // eslint-disable-line

  const overrides = SCAN_FIELDS.filter((f) => scan[f] !== "").length;
  const dirty = SCAN_FIELDS.some((f) => scan[f] !== ((d[f] as string) || "")) || label !== (d.label || "");

  const save = (cfg: DeviceConfig, msg: string) => api.putDevice(d.serial, cfg).then(onChange).then(() => toast.show(msg, "good")).catch((e) => toast.show(String(e), "bad"));
  const toggleEnabled = (v: boolean) => { setEnabled(v); save({ enabled: v, label, ...scan }, v ? "Dongle enabled" : "Dongle disabled"); };
  const applyScan = () => save({ enabled, label, ...scan }, "Applied — dongle restarting");
  const useDefaults = () => { const blank: ScanSettings = { freq: "", gain: "", ppm: "", msgtype: "", filterid: "" }; setScan(blank); save({ enabled, label, ...blank }, "Reset to defaults"); };

  return (
    <div className={"flex flex-col rounded-lg border border-border bg-surface shadow-card transition " + (enabled ? "" : "opacity-60")}>
      <div className="flex items-start justify-between gap-2 border-b border-border p-4">
        <div className="min-w-0">
          <div className="truncate font-medium text-text">{label || d.name || "RTL-SDR"}</div>
          <div className="mono text-micro text-tertiary">{d.serial} · dev{d.dev_index}{d.tuner ? ` · ${d.tuner}` : ""}</div>
        </div>
        <Toggle checked={enabled} onChange={toggleEnabled} />
      </div>

      <div className="flex flex-wrap items-center gap-2 px-4 py-2.5">
        <Badge tone={d.alive ? "good" : "bad"}><Dot tone={d.alive ? "good" : "bad"} /> {d.alive ? "live" : "idle"}</Badge>
        <Badge>{liveRate || d.packets_last_min}/min</Badge>
        <Badge>{d.meters_heard} meters</Badge>
        <Badge tone={overrides ? "info" : "default"}>{overrides ? `${overrides} of ${SCAN_FIELDS.length} custom` : "all inherited"}</Badge>
        <span className="ml-auto text-micro text-tertiary">last {shortTs(d.last_seen)}</span>
      </div>

      <div className="space-y-3 border-t border-border bg-app/30 p-4">
        <p className="text-micro text-tertiary">Each setting is inherited from the capture defaults until you override it for this dongle.</p>
        <div className="grid gap-2.5 sm:grid-cols-2">
          {(["freq", "gain", "ppm", "filterid"] as (keyof ScanSettings)[]).map((f) => (
            <OverrideField key={f} field={f} value={scan[f]} def={defaults[f]} onChange={(v) => setScan({ ...scan, [f]: v })} />
          ))}
        </div>
        <OverrideField field="msgtype" value={scan.msgtype} def={defaults.msgtype} onChange={(v) => setScan({ ...scan, msgtype: v })} />
        <Field label="Label"><Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="friendly name" /></Field>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="primary" disabled={!dirty} icon={<Save size={13} />} onClick={applyScan} success="Applied — dongle restarting">Apply changes</Button>
          <Button size="sm" variant="ghost" disabled={!overrides && label === (d.label || "")} icon={<RotateCcw size={13} />} onClick={useDefaults}>Use all defaults</Button>
          {dirty && <span className="ml-auto text-micro text-warn">unsaved changes</span>}
        </div>
      </div>
    </div>
  );
}

function Coverage() {
  const t = useChartTheme();
  const [cells, setCells] = useState<CoverageCell[] | null>(null);
  useEffect(() => { api.coverage().then(setCells).catch(() => setCells([])); }, []);
  if (!cells) return <Card><CardBody><Skeleton className="h-24" /></CardBody></Card>;
  if (!cells.length) return null;
  const sources = [...new Set(cells.map((c) => c.source))].sort();
  const get: Record<string, number> = {}; let max = 1;
  const meterTotal: Record<number, number> = {};
  cells.forEach((c) => { get[`${c.source}-${c.endpoint_id}`] = c.packets; max = Math.max(max, c.packets); meterTotal[c.endpoint_id] = (meterTotal[c.endpoint_id] || 0) + c.packets; });
  // sort meters by total packets (strongest reception first) — never an unsorted matrix
  const meters = [...new Set(cells.map((c) => c.endpoint_id))].sort((a, b) => (meterTotal[b] || 0) - (meterTotal[a] || 0));
  const shown = meters.slice(0, 60);
  const perSource = sources.map((s) => ({ s, total: cells.filter((c) => c.source === s).reduce((a, c) => a + c.packets, 0), heard: new Set(cells.filter((c) => c.source === s).map((c) => c.endpoint_id)).size }));
  return (
    <Card>
      <CardHeader title="Reception coverage" icon={<SatelliteDish size={16} />} subtitle="Which dongle hears which meter (all-time, strongest first). Use it to place and aim your SDRs." />
      <CardBody>
        <div className="mb-3 grid grid-cols-2 gap-3 sm:grid-cols-3">
          {perSource.map((p) => (
            <div key={p.s} className="rounded-md border border-border bg-app/40 px-3 py-2">
              <div className="label mono">{p.s}</div><div className="mt-1 text-h2 tabular-nums text-text">{p.heard}</div>
              <div className="text-micro text-tertiary">{fmt(p.total)} pkts · meters heard</div>
            </div>
          ))}
        </div>
        <div className="mb-2 flex items-center gap-2 text-micro text-tertiary">
          <span>fewer</span>
          <span className="flex h-2.5 w-28 overflow-hidden rounded-full">
            {[0.15, 0.35, 0.55, 0.75, 0.95].map((a) => <span key={a} className="flex-1" style={{ background: t.heat(a) }} />)}
          </span>
          <span>more packets</span>
          {meters.length > shown.length && <span className="ml-auto">showing {shown.length} of {meters.length} meters</span>}
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-micro">
            <thead><tr className="text-left text-tertiary"><th className="py-1 pr-3 font-medium">meter</th>{sources.map((s) => <th key={s} className="mono px-2 py-1 text-center font-medium">{s}</th>)}</tr></thead>
            <tbody>
              {shown.map((m) => (
                <tr key={m} className="border-t border-border/50">
                  <td className="mono py-1 pr-3 text-secondary">#{m}</td>
                  {sources.map((s) => { const v = get[`${s}-${m}`] || 0; return (
                    <td key={s} className="px-1 py-1 text-center">
                      <span className="inline-block w-full rounded px-1 tabular-nums text-text" style={{ background: v ? t.heat(0.12 + (v / max) * 0.8) : "transparent" }}>{v || "·"}</span>
                    </td>); })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </CardBody>
    </Card>
  );
}
