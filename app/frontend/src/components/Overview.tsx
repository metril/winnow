import { api, Meter, useAsync } from "../api";
import { useLive } from "../App";
import { fmt, shortTs } from "../util";

// Overview: live cards for tracked/published meters + capture health.
export default function Overview() {
  const { tick } = useLive();
  const tracked = useAsync(() => api.meters("?tracked_only=true&include_ignored=true"), "t" + tick);
  const health = useAsync(api.health, "h" + tick);

  const meters = tracked.data || [];
  return (
    <div>
      <div className="panel">
        <h2>Capture</h2>
        {health.data ? (
          <div className="controls">
            <span className="badge"><span className={"dot " + (health.data.alive ? "ok" : "bad")} />
              {health.data.alive ? "alive" : "down"}</span>
            <span className="badge">{health.data.unique_meters} meters seen</span>
            <span className="badge">{health.data.packets_last_min} pkts/min</span>
            {health.data.sources.map((s) => (
              <span key={s.source} className="badge" title={`last ${shortTs(s.last_ts)}`}>
                <span className={"dot " + (s.alive ? "ok" : "bad")} /> {s.source}: {s.packets_last_min}/min
              </span>
            ))}
          </div>
        ) : <span className="muted">loading…</span>}
      </div>

      <h2>My meters {meters.length ? `(${meters.length})` : ""}</h2>
      {!meters.length && (
        <div className="panel muted">
          No meters tracked yet. Go to <strong>Identify</strong>, switch your known load on/off,
          and lock the meter that tracks it.
        </div>
      )}
      <div className="cards">
        {meters.map((m) => <MeterCard key={m.endpoint_id} m={m} />)}
      </div>
    </div>
  );
}

function MeterCard({ m }: { m: Meter }) {
  return (
    <div className="panel card">
      <div className="card-head">
        <strong>{m.pub_name || m.label || `Meter ${m.endpoint_id}`}</strong>
        <span className={"chip " + (m.commodity === "electric" ? "electric" : "")}>{m.commodity}</span>
      </div>
      <div className="muted">#{m.endpoint_id} · {m.msg_type}</div>
      <table className="kv">
        <tbody>
          <tr><td>latest</td><td className="num">{fmt(m.latest_consumption)}</td></tr>
          <tr><td>movement (24h)</td><td className="num">{fmt(m.total_movement)}</td></tr>
          <tr><td>packets/hr</td><td className="num">{fmt(m.packets_per_hour, 1)}</td></tr>
          <tr><td>sources</td><td className="num">{m.sources}</td></tr>
        </tbody>
      </table>
      <div className="card-foot">
        {m.publish
          ? <span className="chip electric" title={`sensor.winnow_${m.endpoint_id}_energy`}>▲ publishing to HA</span>
          : <span className="muted">not published</span>}
      </div>
    </div>
  );
}
