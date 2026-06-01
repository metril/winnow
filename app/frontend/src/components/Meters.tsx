import { useMemo, useState } from "react";
import { api, Meter, useAsync } from "../api";
import { useLive } from "../App";
import { fmt, shortTs, since } from "../util";
import { MultiSeriesChart } from "./charts";
import MeterDetail from "./MeterDetail";
import { Card, SectionTitle, Button, Input, Select, Badge, Toggle, EmptyState, Dialog, useToast } from "../ui";

export default function Meters() {
  const { tick } = useLive();
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

  const { data, error, reload } = useAsync<Meter[]>(() => api.meters(qs), qs + tick);
  const meters = (data || []).filter((m) =>
    !search || String(m.endpoint_id).includes(search) || (m.label || "").toLowerCase().includes(search.toLowerCase()));

  const plot = useAsync(
    () => selected.size ? api.series([...selected], `since=${since(hours)}&bucket=5m&mode=${mode}`) : Promise.resolve({}),
    [...selected].join(",") + mode + hours + tick);

  const toggleSel = (id: number) => setSelected((s) => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n; });
  const patch = (id: number, body: any, msg: string) =>
    api.patchMeter(id, body).then(() => { reload(); toast.show(msg, "good"); }).catch((e) => toast.show(String(e), "bad"));
  const doDelete = () =>
    api.deleteMeter(del!.endpoint_id, purge).then(() => { setDel(null); setPurge(false); setDetail(null); reload(); toast.show(purge ? "Meter purged" : "Meter removed", "good"); });

  const chip = "inline-flex items-center gap-2 text-sm text-muted";
  return (
    <div className="space-y-4">
      <Card>
        <div className="flex flex-wrap items-center gap-3">
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="search id / label" className="max-w-xs" />
          <Select value={hours} onChange={(e) => setHours(+e.target.value)} className="w-24">
            <option value={1}>1h</option><option value={6}>6h</option><option value={24}>24h</option><option value={72}>3d</option><option value={168}>7d</option>
          </Select>
          <label className={chip}><Toggle checked={electric} onChange={setElectric} /> electric</label>
          <label className={chip}><Toggle checked={hideIgnored} onChange={setHideIgnored} /> hide ignored</label>
          <label className={chip}><Toggle checked={trackedOnly} onChange={setTrackedOnly} /> tracked</label>
          <span className="ml-auto text-sm text-faint">{meters.length} meters · {selected.size} selected</span>
        </div>
        {error && <div className="mt-2 text-sm text-bad">{error}</div>}
      </Card>

      {selected.size > 0 && (
        <Card>
          <SectionTitle right={<>
            <Select value={mode} onChange={(e) => setMode(e.target.value as any)} className="w-44">
              <option value="delta">per-bucket usage</option><option value="cumulative">cumulative</option>
            </Select>
            <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>clear</Button>
          </>}>Comparing {selected.size} meters</SectionTitle>
          {plot.data && <MultiSeriesChart data={plot.data} />}
        </Card>
      )}

      <Card className="overflow-hidden p-0">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-surface2/60 text-left text-xs text-muted">
              <tr>
                <th className="px-3 py-2"></th><th className="px-3 py-2 font-medium">meter</th><th className="px-3 py-2 font-medium">commodity</th>
                <th className="px-3 py-2 font-medium">type</th><th className="px-3 py-2 font-medium text-right">pkts/hr</th>
                <th className="px-3 py-2 font-medium text-right">srcs</th><th className="px-3 py-2 font-medium text-right">movement</th>
                <th className="px-3 py-2 font-medium text-right">latest</th><th className="px-3 py-2 font-medium">last seen</th>
                <th className="px-3 py-2 font-medium">actions</th>
              </tr>
            </thead>
            <tbody>
              {meters.map((m) => (
                <tr key={m.endpoint_id} className={"border-t border-border/50 hover:bg-surface2/40 " +
                  (detail === m.endpoint_id ? "bg-surface2/60 " : "") + (m.is_mine ? "ring-1 ring-inset ring-gold/20" : "")}>
                  <td className="px-3 py-2"><input type="checkbox" className="accent-brand" checked={selected.has(m.endpoint_id)} onChange={() => toggleSel(m.endpoint_id)} /></td>
                  <td className="px-3 py-2">
                    <button className="mono hover:text-brand" onClick={() => setDetail(detail === m.endpoint_id ? null : m.endpoint_id)}>#{m.endpoint_id}</button>
                    {m.publish && <Badge tone="gold" className="ml-2">▲ HA</Badge>}
                    {m.is_mine && !m.publish && <Badge tone="brand" className="ml-2">tracked</Badge>}
                    {m.ignored && <Badge className="ml-2">ignored</Badge>}
                    {m.label && <span className="ml-2 text-muted">{m.label}</span>}
                  </td>
                  <td className="px-3 py-2 text-muted">{m.commodity}</td>
                  <td className="px-3 py-2 text-muted">{m.msg_type}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{fmt(m.packets_per_hour, 1)}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{m.sources}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{fmt(m.total_movement)}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-muted">{fmt(m.latest_consumption)}</td>
                  <td className="px-3 py-2 text-faint">{shortTs(m.last_seen)}</td>
                  <td className="px-3 py-2">
                    <div className="flex gap-1">
                      <Button size="sm" variant={m.is_mine ? "primary" : "default"} onClick={() => patch(m.endpoint_id, { is_mine: !m.is_mine }, m.is_mine ? "Untracked" : "Tracked")}>{m.is_mine ? "Tracked" : "Track"}</Button>
                      <Button size="sm" variant={m.publish ? "gold" : "ghost"} onClick={() => patch(m.endpoint_id, { publish: !m.publish, is_mine: true }, m.publish ? "Unpublished" : "Publishing")}>▲</Button>
                      <Button size="sm" variant="ghost" title={m.ignored ? "unignore" : "ignore"} onClick={() => patch(m.endpoint_id, { ignored: !m.ignored }, m.ignored ? "Unignored" : "Ignored")}>{m.ignored ? "↩" : "∅"}</Button>
                      <Button size="sm" variant="ghost" title="delete" onClick={() => { setDel(m); setPurge(false); }}>🗑</Button>
                    </div>
                  </td>
                </tr>
              ))}
              {!meters.length && <tr><td colSpan={10}><EmptyState>No meters in this window.</EmptyState></td></tr>}
            </tbody>
          </table>
        </div>
      </Card>

      {detail !== null && <Card><MeterDetail id={detail} hours={hours} onChange={reload} /></Card>}

      <Dialog open={!!del} onClose={() => setDel(null)} title={`Remove meter #${del?.endpoint_id}`}
        footer={<>
          <Button variant="ghost" onClick={() => setDel(null)}>Cancel</Button>
          <Button variant="danger" success={purge ? "Purged" : "Removed"} onClick={doDelete}>{purge ? "Purge everything" : "Remove"}</Button>
        </>}>
        <p>Removes tracking and annotations for this meter.</p>
        <label className="mt-3 flex items-center gap-2 text-text">
          <input type="checkbox" className="accent-bad" checked={purge} onChange={(e) => setPurge(e.target.checked)} />
          Also <strong>purge stored readings</strong> (irreversible). It reappears only if it broadcasts again.
        </label>
      </Dialog>
    </div>
  );
}
