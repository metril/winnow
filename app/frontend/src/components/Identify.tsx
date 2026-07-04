import { useEffect, useState } from "react";
import { Crosshair, AlertTriangle, Settings, Trophy, Check, X, RotateCcw, CalendarDays, FlaskConical, CheckCircle2, BarChart3 } from "lucide-react";
import { Brush, CartesianGrid, Legend, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { api, CorrRow, DailyMeterRow, IdentifyDaily } from "../api";
import { useLive } from "../live";
import { useFetch } from "../fetch";
import { fmt } from "../util";
import { Page, View } from "./shell";
import { Card, CardHeader, CardBody, Button, Segmented, Badge, EmptyState, FetchError, Skeleton, Table, Th, Td, InfoHint, Input } from "../ui";
import { OverlayChart, ConfidenceBar, brushProps } from "./charts";
import { useChartTheme } from "./chartTheme";
import { TrackStar, PublishToggle } from "./MeterActions";

const RANGES = [{ value: 1, label: "1h" }, { value: 6, label: "6h" }, { value: 24, label: "24h" }, { value: 72, label: "3d" }];
const BUCKETS = [{ value: "auto", label: "auto" }, { value: "1m", label: "1m" }, { value: "5m", label: "5m" }, { value: "15m", label: "15m" }, { value: "60m", label: "1h" }];
const COMMODITIES = [{ value: "electric", label: "Electric" }, { value: "all", label: "All" }];
const MAX_LINES = 8;

async function loadAll(hours: number, bucket: string, commodity: string, selected: number[]) {
  const [d, daily] = await Promise.all([
    api.identify(hours, bucket, commodity),
    api.identifyDaily(selected).catch(() => null as IdentifyDaily | null),
  ]);
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
  return { d, daily, ref, series, shown };
}

export default function Identify({ onNav }: { onNav?: (v: View, p?: (string | number)[]) => void }) {
  const { configVersion } = useLive();
  const [hours, setHours] = useState(6);
  const [bucket, setBucket] = useState("auto");
  const [commodity, setCommodity] = useState("electric");
  const [selected, setSelected] = useState<number[]>([]); // [] → auto top 3
  const [hidden, setHidden] = useState<Set<string>>(new Set()); // legend-hidden among shown
  const { data, error, reload } = useFetch(() => loadAll(hours, bucket, commodity, selected), [hours, bucket, commodity, selected, configVersion]);

  const d = data?.d;
  const ranking: CorrRow[] = d?.ranking || [];
  const noSet = d && !(d.monitored_entities?.length);
  const floor = d?.monitored_floor_w;
  const monitoredKwh: number | undefined = d?.monitored_energy_kwh;
  const cv: number | null = d?.monitored_cv ?? null;
  // A meter with no computable correlation has nil confidence; never crown it.
  const winner = ranking.find((r) => r.confidence != null) || ranking[0];
  const rangeLabel = RANGES.find((x) => x.value === hours)?.label || `${hours}h`;
  const shown = new Set(data?.shown || []);
  // Split the table: measured candidates vs meters with no usable signal.
  let ranked = ranking.filter((r) => r.confidence != null);
  let insufficient = ranking.filter((r) => r.confidence == null);
  if (ranked.length === 0) { ranked = ranking; insufficient = []; } // score-only mode (no reference)

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

      {error && <FetchError error={error} onRetry={reload} />}

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

      {(() => {
        const mine = ranking.find((r) => r.is_mine);
        return mine && (
          <Card variant="alert">
            <CardBody className="flex flex-wrap items-center gap-3">
              <CheckCircle2 size={18} className="text-good" />
              <div className="flex-1 text-small">
                <div className="font-medium text-text">Confirmed: #{mine.endpoint_id} is your meter</div>
                <div className="text-secondary">The hunt is over — this page stays useful for re-checks, but your day-to-day view is Usage.</div>
              </div>
              <Button variant="primary" icon={<BarChart3 size={15} />} onClick={() => onNav?.("usage", [mine.endpoint_id])}>View usage</Button>
            </CardBody>
          </Card>
        );
      })()}

      {!data ? <Skeleton className="h-40" />
        : winner ? <WinnerCard r={winner} monitoredKwh={monitoredKwh} windowLabel={rangeLabel} onReload={reload} confirmed={ranking.some((r) => r.is_mine)} />
          : <Card><CardBody><EmptyState icon={<Crosshair size={22} />} title="No candidates yet">Switch a known load on and off, then Analyze.</EmptyState></CardBody></Card>}

      {data?.daily && data.daily.screen.days.length >= data.daily.screen.min_days && (
        <DailyScreenCard daily={data.daily} selected={selected} onToggle={toggleSelected} cv={cv} onReload={reload} />
      )}

      {data && data.ref.length > 0 && (
        <Card>
          <CardHeader title="Monitored power vs selected candidates"
            subtitle="Your meter's usage should track the yellow line. Tick rows below to add lines; click a legend chip to hide one." />
          <CardBody><OverlayChart reference={data.ref} meters={data.series} hidden={hidden} onToggle={toggleHidden} /></CardBody>
        </Card>
      )}

      {ranking.length > 0 && (
        <Card>
          <CardHeader title="All candidates" subtitle="Ranked by identification confidence. Meters without enough usable signal are grouped at the bottom — they aren't evidence, just silence." />
          <Table>
            <thead><tr>
              <Th className="w-8" /><Th>#</Th><Th>meter</Th><Th>commodity</Th><Th className="w-44">confidence</Th>
              <Th num>calibration</Th><Th num>baseline</Th><Th>floor</Th><Th num>pkts</Th><Th>actions</Th>
            </tr></thead>
            <tbody>
              {ranked.slice(0, 15).map((r, i) => (
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
          {insufficient.length > 0 && (
            <CardBody className="border-t border-border/60">
              <InsufficientSignal rows={insufficient} shown={shown} onToggle={toggleSelected} />
            </CardBody>
          )}
        </Card>
      )}
    </Page>
  );
}

// InsufficientSignal groups meters whose window produced no computable
// correlation: they used to score a neutral ~0.19 and bury the real candidates.
function InsufficientSignal({ rows, shown, onToggle }: { rows: CorrRow[]; shown: Set<number>; onToggle: (id: number) => void }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="text-small">
      <button type="button" className="flex items-center gap-2 text-secondary hover:text-text"
        onClick={() => setOpen((o) => !o)} aria-expanded={open}>
        <span className="font-medium">{rows.length} meters with insufficient signal</span>
        <span className="text-tertiary">{open ? "hide" : "show"}</span>
      </button>
      <div className="mt-1 text-micro text-tertiary">
        Not enough variation overlapped your monitored sensors to measure a correlation — usually a meter that barely moved, or too few packets in this window. Tick one to chart it anyway.
      </div>
      {open && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {rows.slice(0, 60).map((r) => (
            <label key={r.endpoint_id} className="inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-border/60 px-2 py-1 text-micro text-secondary hover:bg-raised/50">
              <input type="checkbox" className="h-3 w-3 accent-brand" checked={shown.has(r.endpoint_id)} onChange={() => onToggle(r.endpoint_id)} />
              <span className="mono">#{r.endpoint_id}</span>
              <span className="text-tertiary">{r.window_packets} pkts</span>
            </label>
          ))}
          {rows.length > 60 && <span className="text-micro text-tertiary">… and {rows.length - 60} more</span>}
        </div>
      )}
    </div>
  );
}

// DailyScreenCard renders the daily-reconciliation physics screen: the only
// candidates whose per-day energy contains the monitored subset every single
// day and sits in the bill's magnitude band — plus a chart where ANY meter can
// be plotted against the monitored and bill-estimate lines.
function DailyScreenCard({ daily, selected, onToggle, cv, onReload }: {
  daily: IdentifyDaily; selected: number[]; onToggle: (id: number) => void;
  cv: number | null; onReload: () => void;
}) {
  const ch = useChartTheme();
  const screen = daily.screen;
  const rows = screen.rows;
  const chartRows = rows.filter((r) => r.pass || selected.includes(r.endpoint_id)).slice(0, MAX_LINES);
  const hasFlat = daily.flat_estimate?.some((v) => v != null);
  const hasShaped = daily.shaped_estimate?.some((v) => v != null);

  const dayMaps = chartRows.map((r) => new Map(r.days.map((dd) => [dd.day, dd.kwh])));
  const dataRows = screen.days.map((day, i) => {
    const row: any = { t: day.slice(5), monitored: screen.monitored_kwh[i] };
    if (daily.flat_estimate?.[i] != null) row.flat = daily.flat_estimate[i];
    if (daily.shaped_estimate?.[i] != null) row.shaped = daily.shaped_estimate[i];
    chartRows.forEach((r, k) => { const v = dayMaps[k].get(day); if (v != null) row["m" + r.endpoint_id] = v; });
    return row;
  });
  const dayStart = Math.max(0, dataRows.length - 60);
  const coarse = rows.filter((r) => r.pass && r.unit === 1);

  return (
    <Card>
      <CardHeader title="Daily reconciliation — the physics screen" icon={<CalendarDays size={16} />}
        subtitle={`Your meter must read at least your monitored consumption (${fmt(screen.monitored_avg, 1)} kWh/day) every single day, and land in the ${fmt(screen.band_lo, 0)}–${fmt(screen.band_hi, 0)} kWh/day band${screen.bill_lo != null ? " your bills establish" : ""}. ${screen.survivors} of your meters pass over ${screen.days.length} full days.`}
        actions={<InfoHint>This screen works even when correlation can't: a near-constant monitored load carries no correlation signal, and coarse meters (whole-kWh counters) drown sub-hourly correlation in quantization noise. Day-level energy containment — whole home ⊇ monitored subset — is immune to both, and sharpens automatically with every captured day.</InfoHint>} />
      <CardBody>
        <ResponsiveContainer width="100%" height={280}>
          <LineChart data={dataRows}>
            <CartesianGrid {...ch.gridProps} />
            <XAxis dataKey="t" {...ch.axisX} minTickGap={24} />
            <YAxis tickFormatter={(v) => fmt(v)} {...ch.axisY} />
            <Tooltip contentStyle={ch.tooltipStyle} formatter={(v: any, n: any) => [fmt(v, 2) + " kWh", n]} />
            <Legend />
            <Line name="monitored" dataKey="monitored" stroke={ch.gold} strokeWidth={2}
              dot={{ r: 2 }} activeDot={{ r: 4 }} isAnimationActive={false} />
            {hasFlat && <Line name="bill flat est." dataKey="flat" stroke={ch.axis} strokeDasharray="4 3" strokeWidth={1.5}
              dot={{ r: 2 }} activeDot={{ r: 4 }} isAnimationActive={false} connectNulls />}
            {hasShaped && <Line name="bill shaped est." dataKey="shaped" stroke={ch.faint} strokeDasharray="2 3" strokeWidth={1.5}
              dot={{ r: 2 }} activeDot={{ r: 4 }} isAnimationActive={false} connectNulls />}
            {chartRows.map((r, k) => (
              <Line key={r.endpoint_id} name={`#${r.endpoint_id}`} dataKey={"m" + r.endpoint_id}
                stroke={ch.seriesPalette[k % ch.seriesPalette.length]} strokeWidth={r.pass ? 2 : 1.5}
                dot={{ r: 2 }} activeDot={{ r: 4 }} isAnimationActive={false} connectNulls />
            ))}
            {dataRows.length > 60 &&
              <Brush dataKey="t" {...brushProps(ch)} startIndex={dayStart} />}
          </LineChart>
        </ResponsiveContainer>
        <div className="mt-1 text-micro text-tertiary">
          The true meter's line rides just above the gold monitored line every day. Tick candidates below (or any meter in the tables) to plot them here.
        </div>
      </CardBody>
      {rows.length > 0 && (
        <Table>
          <thead><tr>
            <Th className="w-8" /><Th>meter</Th><Th>verdict</Th><Th num>kWh/day</Th><Th num>unit</Th>
            <Th num>unmonitored remainder</Th><Th>per-day residual</Th><Th num>pkts</Th><Th num>srcs</Th><Th>actions</Th>
          </tr></thead>
          <tbody>
            {rows.slice(0, 10).map((r) => (
              <tr key={r.endpoint_id} className={"border-b border-border/60 hover:bg-raised/50 " + (r.pass && r === rows[0] ? "bg-brand/5" : "")}>
                <Td>
                  <input type="checkbox" className="h-3.5 w-3.5 cursor-pointer accent-brand"
                    checked={selected.includes(r.endpoint_id)} onChange={() => onToggle(r.endpoint_id)}
                    title="Show on chart" aria-label="Show on chart" />
                </Td>
                <Td><span className="id-pill">#{r.endpoint_id}</span>{r.label && <span className="ml-1.5 text-micro text-tertiary">{r.label}</span>}</Td>
                <Td>{r.pass ? <Badge tone="gold">pass</Badge> : <span className="text-micro text-bad" title={r.reason}>{shortReason(r.reason)}</span>}</Td>
                <Td num className="text-text">{r.kwh_per_day != null ? fmt(r.kwh_per_day, 1) : "–"}</Td>
                <Td num className="text-secondary">{r.unit != null ? `${r.unit} kWh` : "–"}</Td>
                <Td num className="text-secondary">{r.resid_mean != null ? <>{fmt(r.resid_mean, 1)} <span className="text-tertiary">± {fmt(r.resid_sd ?? 0, 1)}</span> kWh/d</> : "–"}</Td>
                <Td><ResidChips r={r} /></Td>
                <Td num className="text-secondary">{r.packets}</Td>
                <Td num className="text-secondary">{r.sources}</Td>
                <Td><div className="flex"><TrackStar id={r.endpoint_id} isMine={false} onChange={onReload} /></div></Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <CardBody className="border-t border-border/60">
        <div className="flex flex-wrap items-start gap-x-6 gap-y-1.5 text-micro text-secondary">
          {daily.screen.excluded_days > 0 && (
            <span className="inline-flex items-center gap-1.5">
              <AlertTriangle size={13} className="text-bad" />
              {daily.screen.excluded_days} day{daily.screen.excluded_days === 1 ? "" : "s"} excluded — the monitored
              reference feed wasn't flowing (coverage below {Math.round((daily.screen.coverage_min ?? 0.9) * 100)}%),
              so those days carry no evidence.
            </span>
          )}
          {cv != null && cv < 0.25 && (
            <span className="inline-flex items-center gap-1.5">
              <AlertTriangle size={13} className="text-gold" />
              Your monitored power is nearly constant (CV {fmt(cv, 2)}) — correlation is weak here, so identification leans on this daily screen.
            </span>
          )}
          {coarse.length > 0 && (
            <span className="inline-flex items-center gap-1.5">
              <FlaskConical size={13} className="text-brand" />
              {coarse.length === 1 ? `#${coarse[0].endpoint_id} counts` : "These candidates count"} whole kWh — to confirm with a load test, add ≥2 kWh (e.g. a 1.5 kW heater for 90+ min) on the Load tests page.
            </span>
          )}
        </div>
      </CardBody>
    </Card>
  );
}

// shortReason compresses a screen-failure reason to a table-cell-sized verdict.
function shortReason(reason?: string): string {
  if (!reason) return "fail";
  if (reason.startsWith("reads below")) return "below monitored";
  if (reason.startsWith("magnitude")) return "wrong magnitude";
  if (reason.startsWith("no readings on")) return "gap in coverage";
  if (reason.startsWith("no readings")) return "no readings";
  return "fail";
}

// ResidChips renders one small green/red chip per day: at-or-above monitored vs below.
function ResidChips({ r }: { r: DailyMeterRow }) {
  if (!r.days.length || r.days.every((d) => d.resid == null)) return <span className="text-tertiary">–</span>;
  return (
    <div className="flex flex-wrap gap-0.5">
      {r.days.map((d) => (
        <span key={d.day} title={`${d.day}: ${d.kwh != null ? fmt(d.kwh, 1) : "?"} kWh (${d.resid != null && d.resid >= 0 ? "+" : ""}${d.resid != null ? fmt(d.resid, 1) : "?"} vs monitored)`}
          className={"h-2.5 w-2.5 rounded-[3px] " + (d.resid == null ? "bg-raised" : d.resid >= -1.5 ? "bg-good/70" : "bg-bad/80")} />
      ))}
    </div>
  );
}

// confTitle builds a hover breakdown of the confidence components.
function confTitle(r: CorrRow): string {
  const p = r.confidence_parts || {};
  const bits = [
    r.r != null ? `r=${r.r}` : null,
    p.physics != null ? `physics=${p.physics.toFixed(2)}` : null,
    p.reconciliation != null ? `reconcile=${p.reconciliation.toFixed(2)}` : null,
    p.packets != null ? `packets=${p.packets.toFixed(2)}` : null,
    p.floor != null ? `floor=${p.floor.toFixed(2)}` : null,
    r.lag_buckets != null ? `lag=${r.lag_buckets}b` : null,
    p.snoop != null ? `snoop=${p.snoop.toFixed(2)}` : null,
    p.ref_cv != null ? `ref-cv=${p.ref_cv.toFixed(2)}` : null,
  ].filter(Boolean);
  return bits.join(" · ");
}

// applyCalibration writes a multiplier (kWh per meter-unit) and the kWh unit.
function applyCalibration(r: CorrRow, mult: number) {
  return api.patchMeter(r.endpoint_id, { pub_multiplier: mult, pub_unit: "kWh" });
}

function WinnerCard({ r, monitoredKwh, windowLabel, onReload, confirmed }: { r: CorrRow; monitoredKwh?: number; windowLabel: string; onReload: () => void; confirmed?: boolean }) {
  const conf = r.confidence ?? 0;
  const strong = conf > 0.6;
  const meterKwh = r.meter_energy_kwh;
  return (
    <Card variant={strong ? "accent" : "default"}>
      <CardBody className="flex flex-col gap-4 sm:flex-row sm:items-start">
        <div className="grid h-14 w-14 shrink-0 place-items-center rounded-2xl bg-gold/12 text-gold"><Trophy size={26} /></div>
        <div className="flex-1">
          <div className="flex items-center gap-2 text-small text-secondary">{confirmed ? (r.is_mine ? "Your confirmed meter" : "Top candidate this window") : strong ? "Most likely your meter" : "Top candidate (weak — run a load test)"}</div>
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
