import { useMemo, useState } from "react";
import { Search, Radio, EyeOff, Eye, Trash2, GitCompare, X } from "lucide-react";
import { api, Meter } from "../api";
import { useLive } from "../live";
import { useFetch } from "../fetch";
import { fmt, shortTs, since } from "../util";
import { Page } from "./shell";
import { Card, CardHeader, CardBody, Button, IconButton, Input, Segmented, Badge, Toggle, EmptyState, Dialog, Table, Th, Td, useToast } from "../ui";
import { MultiSeriesChart } from "./charts";
import MeterDetail from "./MeterDetail";
import { TrackStar, PublishToggle } from "./MeterActions";

const RANGES = [{ value: 1, label: "1h" }, { value: 6, label: "6h" }, { value: 24, label: "24h" }, { value: 72, label: "3d" }, { value: 168, label: "7d" }];

export default function Meters() {
  const { configVersion } = useLive();
  const toast = useToast();
  const [hours, setHours] = useState(24);
  const [search, setSearch] = useState("");
  const [electric, setElectric] = useState(false);
  const [hideIgnored, setHideIgnored] = useState(true);
  const [trackedOnly, setTrackedOnly] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [mode, setMode] = useState<"delta" | "cumulative">("delta");
  const [detail, setDetail] = useState<number | null>(null);
  const [del, setDel] = useState<Meter | null>(null);
  const [purge, setPurge] = useState(false);

  const qs = useMemo(() => {
    const p = new URLSearchParams({ since: since(hours) });
    if (electric) p.set("electric_only", "true");
    if (!hideIgnored) p.set("include_ignored", "true");
    if (trackedOnly) p.set("tracked_only", "true");
    return "?" + p.toString();
  }, [hours, electric, hideIgnored, trackedOnly]);

  const { data, error, reload } = useFetch<Meter[]>(() => api.meters(qs), [qs, configVersion]);
  const meters = (data || []).filter((m) => !search || String(m.endpoint_id).includes(search) || (m.label || "").toLowerCase().includes(search.toLowerCase()));

  const plot = useFetch(() => selected.size ? api.series([...selected], `since=${since(hours)}&bucket=5m&mode=${mode}`) : Promise.resolve({}), [[...selected].join(","), mode, hours]);

  const toggleSel = (id: number) => setSelected((s) => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n; });
  const patch = (id: number, body: any, msg: string) => api.patchMeter(id, body).then(() => { reload(); toast.show(msg, "good"); }).catch((e) => toast.show(String(e), "bad"));
  const doDelete = () => api.deleteMeter(del!.endpoint_id, purge).then(() => { setDel(null); setPurge(false); setDetail(null); reload(); toast.show(purge ? "Meter purged" : "Meter removed", "good"); });

  const filterChip = "flex items-center gap-2 text-small text-secondary";
  return (
    <Page title="Meters" breadcrumb={detail ? `Meters / #${detail}` : undefined}
      actions={<>
        <div className="relative"><Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-tertiary" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="search id / label" className="w-48 pl-8" /></div>
        <Segmented options={RANGES} value={hours} onChange={setHours} />
      </>}>

      <Card>
        <CardBody className="flex flex-wrap items-center gap-x-5 gap-y-2 !py-3">
          <label className={filterChip}><Toggle checked={electric} onChange={setElectric} /> electric</label>
          <label className={filterChip}><Toggle checked={hideIgnored} onChange={setHideIgnored} /> hide ignored</label>
          <label className={filterChip}><Toggle checked={trackedOnly} onChange={setTrackedOnly} /> tracked only</label>
          <span className="ml-auto text-small text-tertiary">{meters.length} meters · {selected.size} selected</span>
        </CardBody>
      </Card>

      {selected.size > 0 && (
        <Card>
          <CardHeader title={<span className="inline-flex items-center gap-2"><GitCompare size={16} /> Comparing {selected.size} meters</span>}
            actions={<>
              <Segmented options={[{ value: "delta", label: "usage" }, { value: "cumulative", label: "cumulative" }]} value={mode} onChange={setMode as any} />
              <Button size="sm" variant="ghost" icon={<X size={14} />} onClick={() => setSelected(new Set())}>clear</Button>
            </>} />
          <CardBody>{plot.data && <MultiSeriesChart data={plot.data} connectNulls={mode === "cumulative"} />}</CardBody>
        </Card>
      )}

      <Card>
        {error && <CardBody><div className="text-small text-bad">{error}</div></CardBody>}
        <Table>
          <thead><tr>
            <Th /><Th>meter</Th><Th>commodity</Th><Th>type</Th><Th num>pkts/hr</Th><Th num>srcs</Th>
            <Th num>movement</Th><Th num>latest</Th><Th>last seen</Th><Th>actions</Th>
          </tr></thead>
          <tbody>
            {meters.map((m) => (
              <tr key={m.endpoint_id} className={"border-b border-border/60 hover:bg-raised/50 " + (detail === m.endpoint_id ? "bg-raised " : "") + (m.is_mine ? "shadow-[inset_2px_0_0] shadow-gold/50" : "")}>
                <Td><input type="checkbox" className="accent-brand" checked={selected.has(m.endpoint_id)} onChange={() => toggleSel(m.endpoint_id)} /></Td>
                <Td>
                  <button className="inline-flex items-center gap-2 hover:text-brand" onClick={() => setDetail(detail === m.endpoint_id ? null : m.endpoint_id)}>
                    <span className="id-pill">#{m.endpoint_id}</span>
                    {m.publish && <Badge tone="gold"><Radio size={10} /> HA</Badge>}
                    {m.is_mine && !m.publish && <Badge tone="brand">tracked</Badge>}
                    {m.ignored && <Badge>ignored</Badge>}
                    {m.label && <span className="text-secondary">{m.label}</span>}
                  </button>
                </Td>
                <Td className="text-secondary">{m.commodity}</Td>
                <Td className="text-secondary">{m.msg_type}</Td>
                <Td num>{fmt(m.packets_per_hour, 1)}</Td>
                <Td num>{m.sources}</Td>
                <Td num>{fmt(m.total_movement)}</Td>
                <Td num className="text-secondary">{fmt(m.latest_consumption)}</Td>
                <Td className="text-tertiary">{shortTs(m.last_seen)}</Td>
                <Td>
                  <div className="flex">
                    <TrackStar id={m.endpoint_id} isMine={m.is_mine} onChange={reload} />
                    <PublishToggle id={m.endpoint_id} publish={m.publish} onChange={reload} />
                    <IconButton label={m.ignored ? "unignore" : "ignore"} onClick={() => patch(m.endpoint_id, { ignored: !m.ignored }, m.ignored ? "Unignored" : "Ignored")}>
                      {m.ignored ? <Eye size={15} /> : <EyeOff size={15} />}</IconButton>
                    <IconButton label="delete" danger onClick={() => { setDel(m); setPurge(false); }}><Trash2 size={15} /></IconButton>
                  </div>
                </Td>
              </tr>
            ))}
            {!meters.length && <tr><Td className="!py-0"><div /></Td><td colSpan={9}><EmptyState icon={<Search size={20} />}>No meters in this window.</EmptyState></td></tr>}
          </tbody>
        </Table>
      </Card>

      {detail !== null && <Card><CardBody><MeterDetail id={detail} hours={hours} onChange={reload} /></CardBody></Card>}

      <Dialog open={!!del} onClose={() => setDel(null)} title={`Remove meter #${del?.endpoint_id}`}
        footer={<>
          <Button variant="ghost" onClick={() => setDel(null)}>Cancel</Button>
          <Button variant="danger" onClick={doDelete} success={purge ? "Purged" : "Removed"}>{purge ? "Purge everything" : "Remove"}</Button>
        </>}>
        <p>Removes tracking and annotations for this meter.</p>
        <label className="mt-3 flex items-center gap-2 text-text">
          <input type="checkbox" className="accent-bad" checked={purge} onChange={(e) => setPurge(e.target.checked)} />
          Also <strong>purge stored readings</strong> (irreversible).
        </label>
      </Dialog>
    </Page>
  );
}
