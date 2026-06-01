import { createContext, useContext, useRef, useState } from "react";
import { api, useAsync, useStream } from "./api";
import { ToastProvider, Dot } from "./ui";
import Overview from "./components/Overview";
import Identify from "./components/Identify";
import Meters from "./components/Meters";
import LoadTests from "./components/LoadTests";
import Devices from "./components/Devices";
import Settings from "./components/Settings";

type View = "overview" | "identify" | "meters" | "loadtests" | "devices" | "settings";
const NAV: { id: View; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "identify", label: "Identify" },
  { id: "meters", label: "Meters" },
  { id: "loadtests", label: "Load tests" },
  { id: "devices", label: "Devices" },
  { id: "settings", label: "Settings" },
];

// StreamCtx exposes a throttled live "tick" and the latest monitored power, fed
// by one SSE connection — child views reload on tick (no polling).
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
    const now = Date.now();
    if (now - last.current > 1500) { last.current = now; setTick((t) => t + 1); }
  });

  return (
    <ToastProvider>
      <StreamCtx.Provider value={{ tick, lastPower }}>
        <div className="min-h-full">
          <header className="sticky top-0 z-30 border-b border-border bg-bg/85 backdrop-blur">
            <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5">
              <div className="flex items-center gap-2 font-semibold">
                <span className="grid h-7 w-7 place-items-center rounded-lg bg-brand/15 text-brand">⚡</span>
                <span className="text-[15px] tracking-tight">winnow</span>
              </div>
              <nav className="flex items-center gap-1">
                {NAV.map((n) => (
                  <button key={n.id} onClick={() => setView(n.id)}
                    className={"rounded-lg px-3 py-1.5 text-sm transition " +
                      (view === n.id ? "bg-brand/15 text-brand font-medium" : "text-muted hover:text-text hover:bg-surface2")}>
                    {n.label}
                  </button>
                ))}
              </nav>
              <div className="flex-1" />
              <StatusBar />
            </div>
          </header>
          <main className="mx-auto max-w-7xl px-4 py-5">
            {view === "overview" && <Overview onNav={setView as any} />}
            {view === "identify" && <Identify />}
            {view === "meters" && <Meters />}
            {view === "loadtests" && <LoadTests />}
            {view === "devices" && <Devices />}
            {view === "settings" && <Settings />}
          </main>
        </div>
      </StreamCtx.Provider>
    </ToastProvider>
  );
}

function StatusBar() {
  const { tick } = useLive();
  const health = useAsync(api.health, "h" + tick);
  const status = useAsync(api.status, "s" + Math.floor(tick / 4));
  const cap = health.data?.alive;
  const rate = health.data?.packets_last_min ?? 0;
  const chip = "inline-flex items-center gap-1.5 rounded-md border border-border bg-surface px-2 py-1 text-xs text-muted";
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className={chip}><Dot ok={!!cap} /> capture {cap ? `${rate}/min` : "down"}</span>
      <span className={chip}><Dot ok={!!status.data?.ha_ok} /> HA</span>
      <span className={chip}><Dot ok={!!status.data?.mqtt_ok} /> MQTT</span>
      <span className={chip}>{health.data?.unique_meters ?? 0} meters</span>
    </div>
  );
}
