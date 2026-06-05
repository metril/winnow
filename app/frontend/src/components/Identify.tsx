import { useEffect, useState } from "react";
import { Crosshair, AlertTriangle, Settings, Trophy, Check, X, RotateCcw } from "lucide-react";
import { api, CorrRow } from "../api";
import { useLive } from "../live";
import { useFetch } from "../fetch";
import { fmt } from "../util";
import { Page } from "./shell";
import { Card, CardHeader, CardBody, Button, Segmented, Badge, EmptyState, Skeleton, Table, Th, Td, InfoHint, Input } from "../ui";
import { OverlayChart, ConfidenceBar } from "./charts";
import { TrackStar, PublishToggle } from "./MeterActions";

const RANGES = [{ value: 1, label: "1h" }, { value: 6, label: "6h" }, { value: 24, label: "24h" }, { value: 72, label: "3d" }];
const BUCKETS = [{ value: "auto", label: "auto" }, { value: "1m", label: "1m" }, { value: "5m", label: "5m" }, { value: "15m", label: "15m" }, { value: "60m", label: "1h" }];
const COMMODITIES = [{ value: "electric", label: "Electric" }, { value: "all", label: "All" }];
const MAX_LINES = 8;

async function loadAll(hours: number, bucket: string, commodity: string, selected: number[]) {
  const d = await api.identify(hours, bucket, commodity);
  let ref: any[] = [], series: Record<string, any[]> = {}, shown: number[] = [];
  if (d.ranking?.length) {
    const ids: number[] = d.ranking.map((r: CorrRow) => r.endpoint_id);
    shown = (selected.length ? selected.filter((id) => ids.includes(id)) : ids.slice(0, 3)).slice(0, MAX_LINES);
    if (shown.length) {
      [ref, series] = await Promise.all([api.referenceSeries(d.start, d.end), api.series(shown, `since=${d.start}&bucket=5m&mode=delta`)]);
    } else {
      ref = await api.referenceSeries(d.start, d.end);
    }
  }
  return { d, ref, series, shown };
}

export default function Identify() {
  const { configVersion } = useLive();
  const [hours, setHours] = useState(6);
  const [bucket, setBucket] = useState("auto");
  const [commodity, setCommodity] = useState("electric");
  const [selected, setSelected] = useState<number[]>([]); // [] → auto top 3
  const [hidden, setHidden] = useState<Set<string>>(new Set()); // legend-hidden among shown
  const { data, reload } = useFetch(() => loadAll(hours, bucket, commodity, selected), [hours, bucket, commodity, selected, configVersion]);

  const d = data?.d;
  const ranking: CorrRow[] = d?.ranking || [];
  const noSet = d && !(d.monitored_entities?.length);
  const floor = d?.monitored_floor_w;
  const monitoredKwh: number | undefined = d?.monitored_energy_kwh;
  const winner = ranking[0];
  const rangeLabel = RANGES.find((x) => x.value === hours)?.label || `${hours}h`;
  const shown = new Set(data?.shown || []);

  // Seed the chart selection from the top 3 once a ranking arrives, so the user
  // then has an explicit set to add to / remove from.
  useEffect(() => {
    if (selected.length === 0 && ranking.length) setSelected(ranking.slice(0, 3).map((r) => r.endpoint_id));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ranking.length]);

  const toggleSelected = (id: number) => setSelected((s) => {
    if (s.includes(id)) return s.filter((x) => x !== id);
    if (s.length >= MAX_LINES) return s; // cap
    return [...s, id];
  });
  const toggleHidden = (key: string) => setHidden((h) => {
    const n = new Set(h);
    n.has(key) ? n.delete(key) : n.add(key);
    return n;
  });

  return (
    <Page title="Identify your meter"
      actions={<>
        {floor != null && floor > 0 && <Badge tone="brand">floor {fmt(floor)} W</Badge>}
        <Segmented options={COMMODITIES} value={commodity} onChange={setCommodity} />
        <Segmented options={RANGES} value={hours} onChange={setHours} />
        <Segmented options={BUCKETS} value={bucket} onChange={setBucket} />
        <Button variant="primary" icon={<Crosshair size={15} />} onClick={reload} success="Analyzed">Analyze</Button>
        <InfoHint>Ranks every meter by a composite <b>confidence</b> that its consumption matches your monitored energy — combining correlation, energy reconciliation, calibration stability, packet adequacy and (across test windows) agreement. Switch a known load on/off first, then Analyze.</InfoHint>
      </>}>

      {noSet && (
        <Card variant="alert">
          <CardBody className="flex items-center gap-3">
            <AlertTriangle size={18} className="text-bad" />
            <div className="flex-1 text-small">
              <div className="font-medium text-text">No ground truth selected</div>
              <div className="text-secondary">Pick your Home-Assistant power sensors in Settings so winnow can correlate against real usage.</div>
            </div>
            <Button variant="default" icon={<Settings size={15} />}>Open Settings</Button>
          </CardBody>
        </Card>
      )}

      {!data ? <Skeleton className="h-40" />
        : winner ? <WinnerCard r={winner} monitoredKwh={monitoredKwh} windowLabel={rangeLabel} onReload={reload} />
          : <Card><CardBody><EmptyState icon={<Crosshair size={22} />} title="No candidates yet">Switch a known load on and off, then Analyze.</EmptyState></CardBody></Card>}

      {data && data.ref.length > 0 && (
        <Card>
          <CardHeader title="Monitored power vs selected candidates"
            subtitle="Your meter's usage should track the yellow line. Tick rows below to add lines; click a legend chip to hide one." />
          <CardBody><OverlayChart reference={data.ref} meters={data.series} hidden={hidden} onToggle={toggleHidden} /></CardBody>
        </Card>
      )}

      {ranking.length > 0 && (
        <Card>
          <CardHeader title="All candidates" subtitle="Ranked by identification confidence." />
          <Table>
            <thead><tr>
              <Th className="w-8" /><Th>#</Th><Th>meter</Th><Th>commodity</Th><Th className="w-44">confidence</Th>
              <Th num>calibration</Th><Th num>baseline</Th><Th>floor</Th><Th num>pkts</Th><Th>actions</Th>
            </tr></thead>
            <tbody>
              {ranking.slice(0, 15).map((r, i) => (
                <tr key={r.endpoint_id} className={"border-b border-border/60 hover:bg-raised/50 " + (i === 0 ? "bg-brand/5" : "")}>
                  <Td>
                    <input type="checkbox" className="h-3.5 w-3.5 cursor-pointer accent-brand"
                      checked={shown.has(r.endpoint_id)} onChange={() => toggleSelected(r.endpoint_id)}
                      title="Show on chart" aria-label="Show on chart" />
                  </Td>
                  <Td className="text-tertiary">{i + 1}</Td>
                  <Td><span className="id-pill">#{r.endpoint_id}</span></Td>
                  <Td className="text-secondary">{r.commodity}</Td>
                  <Td><ConfidenceBar r={r.confidence} title={confTitle(r)} /></Td>
                  <Td num>{r.suggested_multiplier ? <Button size="sm" onClick={() => applyCalibration(r, r.suggested_multiplier!).then(reload)} success="Calibration applied">×{r.suggested_multiplier.toPrecision(3)}</Button> : <span className="text-tertiary">–</span>}</Td>
                  <Td num className="text-secondary">{r.baseline_w != null ? `${fmt(r.baseline_w)} W` : "–"}</Td>
                  <Td>{r.floor_ok == null ? <span className="text-tertiary">–</span> : r.floor_ok ? <Check size={14} className="text-good" /> : <X size={14} className="text-bad" />}</Td>
                  <Td num className="text-secondary">{r.window_packets}</Td>
                  <Td><RowActions r={r} onChange={reload} /></Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}
    </Page>
  );
}

// confTitle builds a hover breakdown of the confidence components.
function confTitle(r: CorrRow): string {
  const p = r.confidence_parts || {};
  const bits = [
    r.r != null ? `r=${r.r}` : null,
    p.reconciliation != null ? `reconcile=${p.reconciliation.toFixed(2)}` : null,
    p.packets != null ? `packets=${p.packets.toFixed(2)}` : null,
    p.floor != null ? `floor=${p.floor.toFixed(2)}` : null,
    r.lag_buckets != null ? `lag=${r.lag_buckets}b` : null,
    p.snoop != null ? `snoop=${p.snoop.toFixed(2)}` : null,
  ].filter(Boolean);
  return bits.join(" · ");
}

// applyCalibration writes a multiplier (kWh per meter-unit) and the kWh unit.
function applyCalibration(r: CorrRow, mult: number) {
  return api.patchMeter(r.endpoint_id, { pub_multiplier: mult, pub_unit: "kWh" });
}

function WinnerCard({ r, monitoredKwh, windowLabel, onReload }: { r: CorrRow; monitoredKwh?: number; windowLabel: string; onReload: () => void }) {
  const conf = r.confidence ?? 0;
  const strong = conf > 0.6;
  const meterKwh = r.meter_energy_kwh;
  return (
    <Card variant={strong ? "accent" : "default"}>
      <CardBody className="flex flex-col gap-4 sm:flex-row sm:items-start">
        <div className="grid h-14 w-14 shrink-0 place-items-center rounded-2xl bg-gold/12 text-gold"><Trophy size={26} /></div>
        <div className="flex-1">
          <div className="flex items-center gap-2 text-small text-secondary">{strong ? "Most likely your meter" : "Top candidate (weak — run a load test)"}</div>
          <div className="mt-0.5 flex items-center gap-2">
            <span className="text-h1 mono text-text">#{r.endpoint_id}</span>
            <Badge tone={r.commodity === "electric" ? "gold" : "brand"}>{r.commodity}</Badge>
          </div>
          <div className="mt-2 max-w-md"><ConfidenceBar r={r.confidence} title={confTitle(r)} /></div>
          {(meterKwh != null || (monitoredKwh != null && monitoredKwh > 0)) && (
            <div className="mt-2 text-micro text-secondary">
              over {windowLabel}: monitored <span className="text-text">{fmt(monitoredKwh, 2)} kWh</span>
              {meterKwh != null && <> · this meter ≈ <span className="text-text">{fmt(meterKwh, 2)} kWh</span> at suggested calibration</>}
            </div>
          )}
          <div className="mt-2 flex flex-wrap gap-x-6 gap-y-1 text-micro text-tertiary">
            {r.r != null && <span>correlation r {r.r.toFixed(2)}</span>}
            {r.baseline_w != null && <span>baseline {fmt(r.baseline_w)} W</span>}
            {r.lag_buckets != null && <span>lag {r.lag_buckets} bkt</span>}
            <span>{r.window_packets} packets</span>
          </div>
          <CalibrationControls r={r} onReload={onReload} />
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <TrackStar id={r.endpoint_id} isMine={r.is_mine} onChange={onReload} />
          <PublishToggle id={r.endpoint_id} publish={r.publish} onChange={onReload} />
        </div>
      </CardBody>
    </Card>
  );
}

// CalibrationControls makes calibration legible and reversible: it shows the
// currently-applied multiplier next to the regression-suggested and known-load
// anchor values, accepts a custom multiplier, and resets to 1 — all without
// requiring the meter to be published first.
function CalibrationControls({ r, onReload }: { r: CorrRow; onReload: () => void }) {
  const applied = r.pub_multiplier && r.pub_multiplier !== 1 ? r.pub_multiplier : null;
  const [custom, setCustom] = useState("");

  const apply = (mult: number, unit: string | null = "kWh") =>
    api.patchMeter(r.endpoint_id, { pub_multiplier: mult, pub_unit: unit ?? undefined }).then(onReload);

  return (
    <div className="mt-3 rounded-xl border border-gold/20 bg-gold/[0.04] p-3">
      <div className="flex items-center gap-1.5 text-micro font-medium text-secondary">
        calibration
        <InfoHint>The multiplier converts the raw meter counter to energy: <b>published kWh = raw delta × multiplier</b>. It only affects published / Overview / cost / MQTT values — never your stored readings.</InfoHint>
      </div>
      <div className="mt-1.5 flex flex-wrap items-center gap-x-5 gap-y-1 text-micro">
        <span className="text-tertiary">applied <span className={applied ? "text-text" : "text-tertiary"}>{applied ? `×${applied.toPrecision(3)} ${r.pub_unit || ""}` : "none (×1)"}</span></span>
        {r.suggested_multiplier != null && <span className="text-tertiary">suggested <span className="text-text">×{r.suggested_multiplier.toPrecision(3)}</span></span>}
        {r.anchor_multiplier != null && <span className="text-tertiary">known-load <span className="text-text">×{r.anchor_multiplier.toPrecision(3)}</span></span>}
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        {r.suggested_multiplier != null && <Button size="sm" variant="gold" onClick={() => apply(r.suggested_multiplier!)} success="Applied">Use suggested</Button>}
        {r.anchor_multiplier != null && <Button size="sm" variant="default" onClick={() => apply(r.anchor_multiplier!)} success="Applied">Use known-load</Button>}
        <div className="flex items-center gap-1">
          <Input value={custom} onChange={(e) => setCustom(e.target.value)} placeholder="custom ×" className="w-24" />
          <Button size="sm" variant="default" disabled={!parseFloat(custom)} onClick={() => apply(parseFloat(custom))} success="Applied">Apply</Button>
        </div>
        {applied && <Button size="sm" variant="ghost" icon={<RotateCcw size={13} />} onClick={() => apply(1, null)} success="Reset">Reset</Button>}
      </div>
    </div>
  );
}

function RowActions({ r, onChange }: { r: CorrRow; onChange: () => void }) {
  return (
    <div className="flex">
      <TrackStar id={r.endpoint_id} isMine={r.is_mine} onChange={onChange} />
      <PublishToggle id={r.endpoint_id} publish={r.publish} onChange={onChange} />
    </div>
  );
}
