// Thin typed-ish fetch wrappers + a polling hook.
import { useEffect, useRef, useState } from "react";

export interface Meter {
  endpoint_id: number;
  msg_type: string | null;
  endpoint_type: number | null;
  commodity: string;
  packets: number;
  packets_per_hour: number;
  sources: number;
  first_seen: string;
  last_seen: string;
  latest_consumption: number | null;
  total_movement: number | null;
  label: string | null;
  is_candidate: boolean;
  is_mine: boolean;
  notes: string | null;
}

export interface SourceHealth {
  source: string;
  alive: boolean;
  age_seconds: number | null;
  last_ts: string | null;
  total_count: number;
  packets_last_min: number;
}
export interface Health {
  alive: boolean;
  sources: SourceHealth[];
  unique_meters: number;
  packets_last_min: number;
}

export interface CorrRow {
  endpoint_id: number;
  commodity: string;
  window_delta: number;
  window_rate: number;
  baseline_rate: number;
  score: number;
  window_packets: number;
}

export interface TestWindow {
  id: number;
  label: string;
  start_ts: string;
  end_ts: string | null;
}

async function j<T>(url: string, opts?: RequestInit): Promise<T> {
  const r = await fetch(url, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  if (!r.ok) throw new Error(`${r.status} ${await r.text()}`);
  return r.json();
}

export const api = {
  health: () => j<Health>("/api/health"),
  meters: (qs: string) => j<Meter[]>(`/api/meters${qs}`),
  meter: (id: number, qs: string) => j<any>(`/api/meters/${id}${qs}`),
  filterCmd: (id: number) => j<any>(`/api/meters/${id}/filter-command`),
  patchMeter: (id: number, body: any) =>
    j<any>(`/api/meters/${id}`, { method: "PATCH", body: JSON.stringify(body) }),
  tests: () => j<TestWindow[]>("/api/tests"),
  startTest: (label: string) =>
    j<TestWindow>("/api/tests/start", { method: "POST", body: JSON.stringify({ label }) }),
  stopTest: (id: number) => j<TestWindow>(`/api/tests/${id}/stop`, { method: "POST" }),
  createTest: (label: string, start_ts: string, end_ts: string) =>
    j<TestWindow>("/api/tests", {
      method: "POST",
      body: JSON.stringify({ label, start_ts, end_ts }),
    }),
  deleteTest: (id: number) => j<any>(`/api/tests/${id}`, { method: "DELETE" }),
  correlation: (id: number) => j<any>(`/api/tests/${id}/correlation`),
  combined: () => j<any>("/api/tests/combined"),
};

/** Poll a loader every `ms` and return its latest value (+ manual refresh). */
export function usePoll<T>(loader: () => Promise<T>, ms: number, deps: any[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const loaderRef = useRef(loader);
  loaderRef.current = loader;

  const tick = () =>
    loaderRef
      .current()
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e) => setError(String(e)));

  useEffect(() => {
    tick();
    const h = setInterval(tick, ms);
    return () => clearInterval(h);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  return { data, error, refresh: tick };
}
