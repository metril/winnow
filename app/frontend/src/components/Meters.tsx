import { useMemo, useState } from "react";
import { api, Meter, usePoll } from "../api";
import { fmt, shortTs, since } from "../util";
import MeterDetail from "./MeterDetail";

type SortKey = keyof Meter;

const COLS: { key: SortKey; label: string; num?: boolean }[] = [
  { key: "endpoint_id", label: "meter id" },
  { key: "commodity", label: "commodity" },
  { key: "msg_type", label: "type" },
  { key: "packets", label: "packets", num: true },
  { key: "packets_per_hour", label: "pkts/hr", num: true },
  { key: "sources", label: "srcs", num: true },
  { key: "total_movement", label: "movement", num: true },
  { key: "latest_consumption", label: "latest", num: true },
  { key: "last_seen", label: "last seen" },
];

export default function Meters() {
  const [hours, setHours] = useState(24);
  const [msgType, setMsgType] = useState("");
  const [electric, setElectric] = useState(false);
  const [sortKey, setSortKey] = useState<SortKey>("total_movement");
  const [desc, setDesc] = useState(true);
  const [selected, setSelected] = useState<number | null>(null);

  const qs = useMemo(() => {
    const p = new URLSearchParams({ since: since(hours) });
    if (msgType) p.set("msg_type", msgType);
    if (electric) p.set("electric_only", "true");
    return "?" + p.toString();
  }, [hours, msgType, electric]);

  const { data, error } = usePoll<Meter[]>(() => api.meters(qs), 5000, [qs]);

  const rows = useMemo(() => {
    const list = [...(data || [])];
    list.sort((a, b) => {
      const av = a[sortKey] as any, bv = b[sortKey] as any;
      if (av === bv) return 0;
      if (av === null || av === undefined) return 1;
      if (bv === null || bv === undefined) return -1;
      const r = av > bv ? 1 : -1;
      return desc ? -r : r;
    });
    return list;
  }, [data, sortKey, desc]);

  const onSort = (k: SortKey) => {
    if (k === sortKey) setDesc(!desc);
    else { setSortKey(k); setDesc(true); }
  };

  return (
    <div>
      <div className="controls">
        <label>range</label>
        <select value={hours} onChange={(e) => setHours(+e.target.value)}>
          <option value={1}>1h</option>
          <option value={6}>6h</option>
          <option value={24}>24h</option>
          <option value={72}>3d</option>
          <option value={168}>7d</option>
        </select>
        <label>type</label>
        <select value={msgType} onChange={(e) => setMsgType(e.target.value)}>
          <option value="">all</option>
          <option>SCM</option><option>SCM+</option><option>IDM</option>
        </select>
        <label>
          <input type="checkbox" checked={electric} onChange={(e) => setElectric(e.target.checked)} />
          {" "}electric only
        </label>
        <span className="muted">{rows.length} meters</span>
      </div>
      {error && <div className="error">{error}</div>}

      <div className="panel" style={{ padding: 0 }}>
        <table>
          <thead>
            <tr>
              {COLS.map((c) => (
                <th key={c.key} className={c.num ? "num" : ""} onClick={() => onSort(c.key)}>
                  {c.label}{sortKey === c.key ? (desc ? " ▼" : " ▲") : ""}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((m) => (
              <tr key={m.endpoint_id}
                className={(m.endpoint_id === selected ? "selected " : "") + (m.is_mine ? "mine" : "")}
                onClick={() => setSelected(m.endpoint_id === selected ? null : m.endpoint_id)}>
                <td>{m.endpoint_id}{m.is_mine ? " 🔒" : m.is_candidate ? " ⭐" : ""}
                  {m.label ? <span className="muted"> · {m.label}</span> : null}</td>
                <td>{m.commodity}{m.commodity === "electric" ? <span className="chip electric">⚡</span> : null}</td>
                <td>{m.msg_type}</td>
                <td className="num">{fmt(m.packets)}</td>
                <td className="num">{fmt(m.packets_per_hour, 1)}</td>
                <td className="num">{m.sources}</td>
                <td className="num">{fmt(m.total_movement)}</td>
                <td className="num">{fmt(m.latest_consumption)}</td>
                <td>{shortTs(m.last_seen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {selected !== null && (
        <div className="panel">
          <MeterDetail id={selected} hours={hours} />
        </div>
      )}
    </div>
  );
}
