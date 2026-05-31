import { api, usePoll } from "../api";
import { shortTs } from "../util";

// Capture health banner: SDR liveness, per-source packet flow, unique meters.
export default function Health() {
  const { data } = usePoll(api.health, 5000);
  if (!data) return <div className="health muted">capture health: loading…</div>;
  return (
    <div className="health">
      <span>
        <span className={"dot " + (data.alive ? "ok" : "bad")} />
        {data.alive ? "capture alive" : "capture DOWN"}
      </span>
      <span className="badge">{data.unique_meters} meters seen</span>
      <span className="badge">{data.packets_last_min} pkts/min</span>
      {data.sources.map((s) => (
        <span key={s.source} className="badge" title={`last: ${shortTs(s.last_ts)}`}>
          <span className={"dot " + (s.alive ? "ok" : "bad")} />
          {s.source}: {s.packets_last_min}/min ({s.total_count} total)
        </span>
      ))}
    </div>
  );
}
