import { useState } from "react";
import {
  Database, HardDrive, Rows3, Clock, Gauge, Wrench, Trash2, AlertTriangle,
  Recycle, RefreshCw, Boxes, ListRestart,
} from "lucide-react";
import { api, DBStats, MaintOp, DeleteMode } from "../api";
import { useLive } from "../live";
import { useFetch } from "../fetch";
import { fmt, bytes, spanOf, shortTs } from "../util";
import { Page } from "./shell";
import {
  Card, CardHeader, CardBody, StatCard, Button, Badge, Dialog, Segmented,
  Input, Field, Select, Table, Th, Td, Skeleton, useToast,
} from "../ui";

export default function Maintenance() {
  const { configVersion } = useLive();
  const { data, reload } = useFetch(api.adminStats, [configVersion]);
  return (
    <Page title="Maintenance" breadcrumb="System"
      actions={<Button icon={<RefreshCw size={15} />} onClick={reload}>Refresh</Button>}>
      <Health s={data} />
      <Ops onDone={reload} />
      <DangerZone s={data} onDone={reload} />
    </Page>
  );
}

/* ------------------------------- health ---------------------------------- */
function Health({ s }: { s?: DBStats | null }) {
  if (!s) return <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">{[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-24" />)}</div>;
  const ratio = s.uncompressed_bytes > 0 ? s.compressed_bytes / s.uncompressed_bytes : 0;
  const saved = ratio > 0 && ratio < 1 ? `${(1 / ratio).toFixed(1)}×` : "—";
  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Database size" value={bytes(s.total_bytes)} icon={<HardDrive size={15} />} tone="brand" />
        <StatCard label="Readings" value={fmt(s.reading_rows)} icon={<Rows3 size={15} />} />
        <StatCard label="Time span" value={spanOf(s.oldest_reading, s.newest_reading)} icon={<Clock size={15} />}
          unit={s.newest_reading ? `→ ${shortTs(s.newest_reading).slice(5, 16)}` : ""} />
        <StatCard label="Compression" value={saved} unit={`${s.compressed_chunks}/${s.chunks} chunks`} icon={<Gauge size={15} />} tone="good" />
      </div>

      <Card>
        <CardHeader title="Tables" icon={<Database size={16} />}
          subtitle={`Per-table size and rows. ${s.retention_policy ? `Raw readings auto-drop after ${s.retention_policy}; ` : ""}${s.compression_policy ? `compress after ${s.compression_policy}.` : ""}`} />
        <Table>
          <thead><tr><Th>table</Th><Th num>size</Th><Th num>rows</Th><Th num>% of db</Th></tr></thead>
          <tbody>
            {s.tables.map((t) => (
              <tr key={t.name} className="border-b border-border/60">
                <Td><span className="mono text-secondary">{t.name}</span></Td>
                <Td num>{bytes(t.bytes)}</Td>
                <Td num className="text-secondary">{fmt(t.rows)}</Td>
                <Td num className="text-tertiary">{s.total_bytes > 0 ? `${Math.round((t.bytes / s.total_bytes) * 100)}%` : "–"}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      </Card>

      {s.sources.length > 0 && (
        <Card>
          <CardHeader title="Readings by source" icon={<Boxes size={16} />} subtitle="How much each dongle has contributed (useful before deleting by source)." />
          <Table>
            <thead><tr><Th>source</Th><Th num>rows</Th><Th>oldest</Th><Th>newest</Th></tr></thead>
            <tbody>
              {s.sources.map((src) => (
                <tr key={src.source} className="border-b border-border/60">
                  <Td><span className="id-pill">{src.source}</span></Td>
                  <Td num>{fmt(src.rows)}</Td>
                  <Td className="text-tertiary">{src.oldest ? shortTs(src.oldest).slice(0, 16) : "–"}</Td>
                  <Td className="text-tertiary">{src.newest ? shortTs(src.newest).slice(0, 16) : "–"}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}
    </>
  );
}

/* ----------------------------- maintenance ------------------------------- */
function Ops({ onDone }: { onDone: () => void }) {
  const run = (op: MaintOp) => () => api.maintenance(op).then(onDone);
  const ops: { op: MaintOp; label: string; icon: any; desc: string }[] = [
    { op: "vacuum", label: "VACUUM ANALYZE", icon: Recycle, desc: "Reclaim space & refresh planner stats." },
    { op: "reindex", label: "Reindex", icon: ListRestart, desc: "Rebuild registry indexes." },
    { op: "refresh_agg", label: "Refresh aggregate", icon: RefreshCw, desc: "Re-materialize the 1-minute rollup." },
    { op: "compress", label: "Compress chunks", icon: Boxes, desc: "Compress chunks past the 7-day horizon." },
    { op: "prune_devices", label: "Prune devices", icon: Wrench, desc: "Drop inventory for departed dongles." },
  ];
  return (
    <Card>
      <CardHeader title="Maintenance" icon={<Wrench size={16} />} subtitle="Safe, idempotent operations. Some briefly lock the readings table." />
      <CardBody className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {ops.map(({ op, label, icon: Icon, desc }) => (
          <div key={op} className="flex flex-col gap-2 rounded-md border border-border bg-app/40 p-3">
            <div className="flex items-center gap-2 text-small font-medium text-text"><Icon size={15} className="text-secondary" />{label}</div>
            <p className="text-micro text-tertiary">{desc}</p>
            <Button size="sm" className="mt-auto self-start" onClick={run(op)} success={`${label} done`}>Run</Button>
          </div>
        ))}
      </CardBody>
    </Card>
  );
}

/* ------------------------------ danger zone ------------------------------ */
const MODES: { value: DeleteMode; label: string }[] = [
  { value: "age", label: "Older than…" },
  { value: "source", label: "By dongle" },
  { value: "all_tests", label: "Test windows" },
  { value: "all_readings", label: "Purge all" },
];

function DangerZone({ s, onDone }: { s?: DBStats | null; onDone: () => void }) {
  const toast = useToast();
  const [mode, setMode] = useState<DeleteMode>("age");
  const [days, setDays] = useState("90");
  const [source, setSource] = useState("");
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");

  const sources = s?.sources.map((x) => x.source) ?? [];
  const token = mode === "all_readings" ? "PURGE-ALL" : "DELETE";
  const needsType = mode === "all_readings";

  const describe = (): string => {
    switch (mode) {
      case "age": return `Delete all readings older than ${days || 0} days. Whole chunks are dropped; this cannot be undone.`;
      case "source": {
        const row = s?.sources.find((x) => x.source === source);
        return `Delete every reading from source “${source || "—"}”${row ? ` (${fmt(row.rows)} rows)` : ""}. This cannot be undone.`;
      }
      case "all_tests": return `Delete all test-window records. The underlying readings are kept.`;
      case "all_readings": return `Permanently delete ALL ${fmt(s?.reading_rows ?? 0)} readings and the derived rollups. Meter labels and settings are kept. This cannot be undone.`;
    }
  };
  const valid = mode === "age" ? Number(days) > 0 : mode === "source" ? !!source : true;
  const confirmDisabled = needsType && typed !== token;

  const submit = () =>
    api.adminDelete({ mode, days: Number(days) || 0, source, confirm: token })
      .then((r) => { setOpen(false); setTyped(""); toast.show(`Deleted — ${fmt(r.removed)} rows`, "good"); onDone(); })
      .catch((e) => toast.show(String(e), "bad"));

  return (
    <Card className="!border-bad/40">
      <CardHeader title={<span className="inline-flex items-center gap-2"><AlertTriangle size={16} className="text-bad" /> Danger zone</span>}
        subtitle="Irreversible data deletion. Actions here cannot be undone — review the summary before confirming." />
      <CardBody className="space-y-4">
        <Segmented options={MODES} value={mode} onChange={(m) => { setMode(m); setTyped(""); }} />
        <div className="flex flex-wrap items-end gap-3">
          {mode === "age" && <Field label="Age threshold (days)"><Input value={days} onChange={(e) => setDays(e.target.value)} className="w-40" inputMode="numeric" /></Field>}
          {mode === "source" && (
            <Field label="Source / dongle">
              <Select value={source} onChange={(e) => setSource(e.target.value)} className="w-56">
                <option value="">choose a source…</option>
                {sources.map((x) => <option key={x} value={x}>{x}</option>)}
              </Select>
            </Field>
          )}
          <Button variant="danger" icon={<Trash2 size={15} />} disabled={!valid} onClick={() => { setTyped(""); setOpen(true); }}>
            Review &amp; delete
          </Button>
        </div>
        <p className="text-micro text-tertiary">{describe()}</p>
      </CardBody>

      <Dialog open={open} onClose={() => setOpen(false)}
        title={<span className="inline-flex items-center gap-2 text-bad"><AlertTriangle size={16} /> Confirm deletion</span>}
        footer={<>
          <Button variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
          <Button variant="danger" disabled={confirmDisabled} onClick={submit}>
            {mode === "all_readings" ? "Purge everything" : mode === "source" ? "Delete source" : mode === "all_tests" ? "Delete test windows" : `Delete older than ${days}d`}
          </Button>
        </>}>
        <p className="text-text">{describe()}</p>
        {needsType && (
          <div className="mt-3">
            <div className="label mb-1">Type <span className="mono text-bad">{token}</span> to confirm</div>
            <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={token} autoFocus />
          </div>
        )}
      </Dialog>
    </Card>
  );
}
