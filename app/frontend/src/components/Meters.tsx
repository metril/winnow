import { useMemo, useState } from "react";
import { api, Meter, useAsync } from "../api";
import { useLive } from "../App";
import { fmt, shortTs, since } from "../util";
import { MultiSeriesChart } from "./charts";
import MeterDetail from "./MeterDetail";

export default function Meters() {
  const { tick } = useLive();
  const [hours, setHours] = useState(24);
  const [search, setSearch] = useState("");
  const [electric, setElectric] = useState(false);
  const [hideIgnored, setHideIgnored] = useState(true);
  const [trackedOnly, setTrackedOnly] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [mode, setMode] = useState<"delta" | "cumulative">("delta");
  const [detail, setDetail] = useState<number | null>(null);

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

  const toggle = (id: number) => setSelected((s) => {
    const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n;
  });
  const patch = (id: number, body: any) => api.patchMeter(id, body).then(reload);

  return (
    <div>
      <div className="controls">
        <input type="text" placeholder="search id / label" value={search} onChange={(e) => setSearch(e.target.value)} />
        <label>range</label>
        <select value={hours} onChange={(e) => setHours(+e.target.value)}>
          <option value={1}>1h</option><option value={6}>6h</option>
          <option value={24}>24h</option><option value={72}>3d</option><option value={168}>7d</option>
        </select>
        <label><input type="checkbox" checked={electric} onChange={(e) => setElectric(e.target.checked)} /> electric</label>
        <label><input type="checkbox" checked={hideIgnored} onChange={(e) => setHideIgnored(e.target.checked)} /> hide ignored</label>
        <label><input type="checkbox" checked={trackedOnly} onChange={(e) => setTrackedOnly(e.target.checked)} /> tracked</label>
        <span className="muted">{meters.length} meters · {selected.size} selected</span>
      </div>
      {error && <div className="error">{error}</div>}

      {selected.size > 0 && (
        <div className="panel">
          <div className="controls">
            <h3 style={{ margin: 0 }}>Comparing {selected.size} meters</h3>
            <select value={mode} onChange={(e) => setMode(e.target.value as any)}>
              <option value="delta">per-bucket usage</option>
              <option value="cumulative">cumulative</option>
            </select>
            <button className="btn alt" onClick={() => setSelected(new Set())}>clear</button>
          </div>
          {plot.data && <MultiSeriesChart data={plot.data} />}
        </div>
      )}

      <div className="panel" style={{ padding: 0 }}>
        <table>
          <thead><tr>
            <th></th><th>meter</th><th>commodity</th><th>type</th>
            <th className="num">pkts/hr</th><th className="num">srcs</th>
            <th className="num">movement</th><th className="num">latest</th>
            <th>last seen</th><th>flags</th>
          </tr></thead>
          <tbody>
            {meters.map((m) => (
              <tr key={m.endpoint_id} className={(detail === m.endpoint_id ? "selected " : "") + (m.is_mine ? "mine" : "")}>
                <td><input type="checkbox" checked={selected.has(m.endpoint_id)} onChange={() => toggle(m.endpoint_id)} /></td>
                <td style={{ cursor: "pointer" }} onClick={() => setDetail(detail === m.endpoint_id ? null : m.endpoint_id)}>
                  {m.endpoint_id}{m.publish ? " ▲" : m.is_mine ? " 🔒" : ""}
                  {m.label ? <span className="muted"> · {m.label}</span> : null}
                </td>
                <td>{m.commodity}{m.commodity === "electric" ? <span className="chip electric">⚡</span> : null}</td>
                <td>{m.msg_type}</td>
                <td className="num">{fmt(m.packets_per_hour, 1)}</td>
                <td className="num">{m.sources}</td>
                <td className="num">{fmt(m.total_movement)}</td>
                <td className="num">{fmt(m.latest_consumption)}</td>
                <td>{shortTs(m.last_seen)}</td>
                <td style={{ whiteSpace: "nowrap" }}>
                  <button className="mini" title="track" onClick={() => patch(m.endpoint_id, { is_mine: !m.is_mine })}>{m.is_mine ? "★" : "☆"}</button>
                  <button className="mini" title="publish" onClick={() => patch(m.endpoint_id, { publish: !m.publish, is_mine: true })}>{m.publish ? "▲" : "△"}</button>
                  <button className="mini" title="ignore" onClick={() => patch(m.endpoint_id, { ignored: !m.ignored })}>{m.ignored ? "🚫" : "∅"}</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {detail !== null && (
        <div className="panel"><MeterDetail id={detail} hours={hours} onChange={reload} /></div>
      )}
    </div>
  );
}
