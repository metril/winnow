import { useState } from "react";
import { Crosshair, AlertTriangle, Settings, Trophy, Check, X } from "lucide-react";
import { api, CorrRow } from "../api";
import { useLive } from "../live";
import { useFetch } from "../fetch";
import { fmt } from "../util";
import { Page } from "./shell";
import { Card, CardHeader, CardBody, Button, Segmented, Badge, EmptyState, Skeleton, Table, Th, Td, InfoHint } from "../ui";
import { OverlayChart, ConfidenceBar } from "./charts";
import { TrackStar, PublishToggle } from "./MeterActions";

const RANGES = [{ value: 1, label: "1h" }, { value: 6, label: "6h" }, { value: 24, label: "24h" }, { value: 72, label: "3d" }];
const BUCKETS = [{ value: "auto", label: "auto" }, { value: "1m", label: "1m" }, { value: "5m", label: "5m" }, { value: "15m", label: "15m" }, { value: "60m", label: "1h" }];

async function loadAll(hours: number, bucket: string) {
  const d = await api.identify(hours, bucket);
  let ref: any[] = [], series: Record<string, any[]> = {};
  if (d.ranking?.length) {
    const top = d.ranking.slice(0, 3).map((r: CorrRow) => r.endpoint_id);
    [ref, series] = await Promise.all([api.referenceSeries(d.start, d.end), api.series(top, `since=${d.start}&bucket=5m&mode=delta`)]);
  }
  return { d, ref, series };
}

export default function Identify() {
  const { power, configVersion } = useLive();
  const [hours, setHours] = useState(6);
  const [bucket, setBucket] = useState("auto");
  const { data, reload } = useFetch(() => loadAll(hours, bucket), [hours, bucket, configVersion]);

  const d = data?.d;
  const ranking: CorrRow[] = d?.ranking || [];
  const noSet = d && !(d.monitored_entities?.length);
  const floor = d?.monitored_floor_w;
  const monitoredKwh: number | undefined = d?.monitored_energy_kwh;
  const winner = ranking[0];
  const rangeLabel = RANGES.find((x) => x.value === hours)?.label || `${hours}h`;
  const apply = (r: CorrRow) => api.patchMeter(r.endpoint_id, { pub_multiplier: r.suggested_multiplier, pub_unit: "kWh" }).then(reload);

  return (
    <Page title="Identify your meter"
      actions={<>
        {power != null && <Badge tone="gold">monitored {fmt(power)} W</Badge>}
        {floor != null && floor > 0 && <Badge tone="brand">floor {fmt(floor)} W</Badge>}
        <Segmented options={RANGES} value={hours} onChange={setHours} />
        <Segmented options={BUCKETS} value={bucket} onChange={setBucket} />
        <Button variant="primary" icon={<Crosshair size={15} />} onClick={reload} success="Analyzed">Analyze</Button>
        <InfoHint>Re-runs the correlation over the selected window: ranks every meter by how well its consumption tracks your monitored energy. The bucket is the comparison period ("auto" scales with the window). Switch a known load on/off first, then Analyze.</InfoHint>
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
        : winner ? <WinnerCard r={winner} monitoredKwh={monitoredKwh} windowLabel={rangeLabel} onApply={apply} onReload={reload} />
          : <Card><CardBody><EmptyState icon={<Crosshair size={22} />} title="No candidates yet">Switch a known load on and off, then Analyze.</EmptyState></CardBody></Card>}

      {data && data.ref.length > 0 && (
        <Card>
          <CardHeader title="Monitored power vs top candidates" subtitle="Your meter's usage should track the yellow line." />
          <CardBody><OverlayChart reference={data.ref} meters={data.series} /></CardBody>
        </Card>
      )}

      {ranking.length > 0 && (
        <Card>
          <CardHeader title="All candidates" subtitle="Ranked by correlation with your monitored power." />
          <Table>
            <thead><tr>
              <Th>#</Th><Th>meter</Th><Th>commodity</Th><Th className="w-44">correlation</Th>
              <Th num>calibration</Th><Th num>baseline</Th><Th>floor</Th><Th num>pkts</Th><Th>actions</Th>
            </tr></thead>
            <tbody>
              {ranking.slice(0, 15).map((r, i) => (
                <tr key={r.endpoint_id} className={"border-b border-border/60 hover:bg-raised/50 " + (i === 0 ? "bg-brand/5" : "")}>
                  <Td className="text-tertiary">{i + 1}</Td>
                  <Td><span className="id-pill">#{r.endpoint_id}</span></Td>
                  <Td className="text-secondary">{r.commodity}</Td>
                  <Td><ConfidenceBar r={r.r} /></Td>
                  <Td num>{r.suggested_multiplier ? <Button size="sm" onClick={() => apply(r)} success="Calibration applied">×{r.suggested_multiplier.toPrecision(3)}</Button> : <span className="text-tertiary">–</span>}</Td>
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

function WinnerCard({ r, monitoredKwh, windowLabel, onApply, onReload }: { r: CorrRow; monitoredKwh?: number; windowLabel: string; onApply: (r: CorrRow) => Promise<any>; onReload: () => void }) {
  const conf = r.r ?? 0;
  const strong = conf > 0.5;
  // Energy reconciliation: this candidate's consumption over the window, in kWh,
  // at the suggested calibration — should be ≥ the monitored subset's energy.
  const meterKwh = r.suggested_multiplier != null ? r.window_delta * r.suggested_multiplier : null;
  return (
    <Card variant={strong ? "accent" : "default"}>
      <CardBody className="flex flex-col gap-4 sm:flex-row sm:items-center">
        <div className="grid h-14 w-14 shrink-0 place-items-center rounded-2xl bg-gold/12 text-gold"><Trophy size={26} /></div>
        <div className="flex-1">
          <div className="flex items-center gap-2 text-small text-secondary">{strong ? "Most likely your meter" : "Top candidate (weak — run a load test)"}</div>
          <div className="mt-0.5 flex items-center gap-2">
            <span className="text-h1 mono text-text">#{r.endpoint_id}</span>
            <Badge tone={r.commodity === "electric" ? "gold" : "brand"}>{r.commodity}</Badge>
          </div>
          <div className="mt-2 max-w-md"><ConfidenceBar r={r.r} /></div>
          {(meterKwh != null || (monitoredKwh != null && monitoredKwh > 0)) && (
            <div className="mt-2 text-micro text-secondary">
              over {windowLabel}: monitored <span className="text-text">{fmt(monitoredKwh, 2)} kWh</span>
              {meterKwh != null && <> · this meter ≈ <span className="text-text">{fmt(meterKwh, 2)} kWh</span> at suggested calibration</>}
            </div>
          )}
          <div className="mt-2 flex flex-wrap gap-x-6 gap-y-1 text-micro text-tertiary">
            {r.suggested_multiplier && <span>suggested ×{r.suggested_multiplier.toPrecision(3)} kWh/unit</span>}
            {r.baseline_w != null && <span>baseline {fmt(r.baseline_w)} W</span>}
            <span>{r.window_packets} packets</span>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {r.suggested_multiplier && <Button variant="default" onClick={() => onApply(r)} success="Calibration applied">Calibrate</Button>}
          <TrackStar id={r.endpoint_id} isMine={r.is_mine} onChange={onReload} />
          <PublishToggle id={r.endpoint_id} publish={r.publish} onChange={onReload} />
        </div>
      </CardBody>
    </Card>
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
