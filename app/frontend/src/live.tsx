// Event-driven live store. The single SSE connection pushes live values; there
// are NO timers and NO polling. High-frequency `reading` events are coalesced
// via requestAnimationFrame (scheduled only when events arrive).
//
// TWO contexts, deliberately: the fast one changes on every packet/reference
// sample (at production rates that's several times a second), the meta one only
// on config/agent events. A page that merely wants configVersion must NOT
// re-render per packet — that's what made the Utility page's Brush undraggable
// once capture hit 300+ packets/min: every event rebuilt the chart under the
// user's cursor. Subscribe with useLiveMeta() unless you render live data.
import { createContext, useContext, useEffect, useMemo, useRef, useState, ReactNode } from "react";
import { useStream } from "./api";

export interface PowerSample { t: number; v: number }
export interface ReadingSample { t: number; source: string }

interface LiveFast {
  power: number | null;        // latest total monitored power (W)
  powerHistory: PowerSample[]; // rolling buffer for the hero sparkline
  readings: ReadingSample[];   // rolling buffer of recent reading events
  connectedAt: number;         // epoch ms when this client connected (reset on refresh)
}

export interface LiveMeta {
  configVersion: number; // bumps on a `config` SSE event
  agentVersion: number;  // bumps on an `agent` SSE event (pending/connect)
}

const FastCtx = createContext<LiveFast>({ power: null, powerHistory: [], readings: [], connectedAt: 0 });
const MetaCtx = createContext<LiveMeta>({ configVersion: 0, agentVersion: 0 });

// useLiveMeta: config/agent counters only — safe for chart pages.
export const useLiveMeta = () => useContext(MetaCtx);
// useLive: full live store (re-renders per event) + the meta counters.
export const useLive = () => {
  const fast = useContext(FastCtx);
  const meta = useContext(MetaCtx);
  return { ...fast, ...meta };
};

const MAX_POWER = 180;   // ~ last N monitored-power samples
const READ_WINDOW = 120_000; // keep reading events from the last 2 min

export function LiveProvider({ children }: { children: ReactNode }) {
  const [power, setPower] = useState<number | null>(null);
  const [powerHistory, setPowerHistory] = useState<PowerSample[]>([]);
  const [readings, setReadings] = useState<ReadingSample[]>([]);
  const [configVersion, setConfigVersion] = useState(0);
  const [agentVersion, setAgentVersion] = useState(0);
  const [connectedAt] = useState(() => Date.now());

  // coalesce bursts of reading events into one commit per animation frame
  const pending = useRef<ReadingSample[]>([]);
  const raf = useRef(0);
  const flush = () => {
    raf.current = 0;
    const add = pending.current;
    pending.current = [];
    if (!add.length) return;
    const cut = Date.now() - READ_WINDOW;
    setReadings((r) => [...r, ...add].filter((x) => x.t >= cut));
  };

  useStream((e) => {
    const now = Date.now();
    if (e.type === "reference") {
      setPower(e.power);
      setPowerHistory((h) => [...h, { t: now, v: e.power }].slice(-MAX_POWER));
    } else if (e.type === "reading") {
      pending.current.push({ t: now, source: e.source || "" });
      if (!raf.current) raf.current = requestAnimationFrame(flush);
    } else if (e.type === "config") {
      setConfigVersion((c) => c + 1);
    } else if (e.type === "agent") {
      setAgentVersion((c) => c + 1);
    }
  });

  useEffect(() => () => { if (raf.current) cancelAnimationFrame(raf.current); }, []);

  const meta = useMemo(() => ({ configVersion, agentVersion }), [configVersion, agentVersion]);
  const fast = useMemo(() => ({ power, powerHistory, readings, connectedAt }),
    [power, powerHistory, readings, connectedAt]);
  return (
    <MetaCtx.Provider value={meta}>
      <FastCtx.Provider value={fast}>{children}</FastCtx.Provider>
    </MetaCtx.Provider>
  );
}

// perMin counts reading events in the last 60s (optionally for one source).
export function perMin(readings: ReadingSample[], source?: string): number {
  const cut = Date.now() - 60_000;
  let n = 0;
  for (const r of readings) if (r.t >= cut && (!source || r.source === source)) n++;
  return n;
}

// activeSources returns the set of sources heard in the last 60s.
export function activeSources(readings: ReadingSample[]): string[] {
  const cut = Date.now() - 60_000;
  const s = new Set<string>();
  for (const r of readings) if (r.t >= cut && r.source) s.add(r.source);
  return [...s];
}
