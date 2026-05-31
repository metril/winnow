import { createContext, useContext, useRef, useState } from "react";
import { api, useAsync, useStream } from "./api";
import Overview from "./components/Overview";
import Identify from "./components/Identify";
import Meters from "./components/Meters";
import Settings from "./components/Settings";

type View = "overview" | "identify" | "meters" | "settings";

// StreamCtx exposes a throttled live "tick" and the latest plug power, fed by a
// single SSE connection — child views reload on tick (no polling).
interface Stream { tick: number; lastPower: number | null }
const StreamCtx = createContext<Stream>({ tick: 0, lastPower: null });
export const useLive = () => useContext(StreamCtx);

export default function App() {
  const [view, setView] = useState<View>("overview");
  const [tick, setTick] = useState(0);
  const [lastPower, setLastPower] = useState<number | null>(null);
  const last = useRef(0);

  useStream((e) => {
    if (e.type === "reference") setLastPower(e.power);
    // throttle reading-driven reloads to ~1.5s
    const now = Date.now();
    if (now - last.current > 1500) { last.current = now; setTick((t) => t + 1); }
  });

  return (
    <StreamCtx.Provider value={{ tick, lastPower }}>
      <header className="topbar">
        <h1>⚡ winnow</h1>
        <nav>
          {(["overview", "identify", "meters", "settings"] as View[]).map((v) => (
            <button key={v} className={view === v ? "active" : ""} onClick={() => setView(v)}>
              {v[0].toUpperCase() + v.slice(1)}
            </button>
          ))}
        </nav>
        <div style={{ flex: 1 }} />
        <StatusChips />
      </header>
      <main>
        {view === "overview" && <Overview />}
        {view === "identify" && <Identify />}
        {view === "meters" && <Meters />}
        {view === "settings" && <Settings />}
      </main>
    </StreamCtx.Provider>
  );
}

function StatusChips() {
  const { tick } = useLive();
  const health = useAsync(api.health, "h" + tick);
  const status = useAsync(api.status, "s" + Math.floor(tick / 4)); // status changes slowly
  const cap = health.data?.alive;
  const rate = health.data?.packets_last_min ?? 0;
  return (
    <div className="chips">
      <span className="chip" title="capture">
        <span className={"dot " + (cap ? "ok" : "bad")} /> capture {cap ? `${rate}/min` : "down"}
      </span>
      <span className="chip"><span className={"dot " + (status.data?.ha_ok ? "ok" : "bad")} /> HA</span>
      <span className="chip"><span className={"dot " + (status.data?.mqtt_ok ? "ok" : "bad")} /> MQTT</span>
      <span className="chip">{health.data?.unique_meters ?? 0} meters</span>
    </div>
  );
}
