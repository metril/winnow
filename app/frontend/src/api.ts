// winnow API client + SSE stream hook (no polling).
import { useEffect, useRef, useState } from "react";

export interface Meter {
  endpoint_id: number;
  msg_type: string;
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
  notes: string | null;
  is_candidate: boolean;
  is_mine: boolean;
  ignored: boolean;
  publish: boolean;
  pub_name: string | null;
  pub_multiplier: number;
  pub_unit: string | null;
}

export interface CorrRow {
  endpoint_id: number;
  commodity: string;
  msg_type: string;
  endpoint_type: number | null;
  r: number | null;
  score: number;
  window_delta: number;
  window_rate: number;
  baseline_rate: number;
  window_packets: number;
  plug_energy_wh: number | null;
}

export interface TestWindow {
  id: number;
  label: string;
  start_ts: string;
  end_ts: string | null;
  source: string;
}

export interface SourceHealth {
  source: string; alive: boolean; age_seconds: number | null;
  last_ts: string | null; total_count: number; packets_last_min: number;
}
export interface Health {
  alive: boolean; sources: SourceHealth[]; unique_meters: number; packets_last_min: number;
}

export interface Status {
  ha_ok: boolean; mqtt_ok: boolean; reference_entity: string; published: Meter[];
}

export interface PowerEntity { entity_id: string; name: string; state: string; unit: string; }

async function j<T>(url: string, opts?: RequestInit): Promise<T> {
  const r = await fetch(url, { headers: { "Content-Type": "application/json" }, ...opts });
  if (!r.ok) throw new Error(`${r.status} ${await r.text()}`);
  return r.json();
}

export const api = {
  health: () => j<Health>("/api/health"),
  status: () => j<Status>("/api/integrations/status"),
  meters: (qs: string) => j<Meter[]>(`/api/meters${qs}`),
  meter: (id: number, qs: string) => j<any>(`/api/meters/${id}${qs}`),
  patchMeter: (id: number, body: any) =>
    j<Meter>(`/api/meters/${id}`, { method: "PATCH", body: JSON.stringify(body) }),
  filterCmd: (id: number) => j<any>(`/api/meters/${id}/filter-command`),
  series: (ids: number[], qs: string) =>
    j<Record<string, { bucket: string; value: number }[]>>(`/api/series?ids=${ids.join(",")}&${qs}`),

  settings: () => j<Record<string, any>>("/api/settings"),
  putSettings: (body: Record<string, string>) =>
    j<any>("/api/settings", { method: "PUT", body: JSON.stringify(body) }),
  testIntegrations: (body: Record<string, string>) =>
    j<any>("/api/integrations/test", { method: "POST", body: JSON.stringify(body) }),
  powerEntities: () => j<PowerEntity[]>("/api/ha/power-entities"),

  identify: (hours: number) => j<any>(`/api/identify?hours=${hours}`),
  identifyAuto: () => j<any>("/api/identify/auto"),
  referenceSeries: (startISO: string, endISO: string) =>
    j<{ bucket: string; value: number }[]>(`/api/reference/series?start=${startISO}&end=${endISO}`),

  tests: () => j<TestWindow[]>("/api/tests"),
  startTest: (label: string) =>
    j<TestWindow>("/api/tests/start", { method: "POST", body: JSON.stringify({ label }) }),
  stopTest: (id: number) => j<TestWindow>(`/api/tests/${id}/stop`, { method: "POST" }),
  createTest: (label: string, start_ts: string, end_ts: string) =>
    j<TestWindow>("/api/tests", { method: "POST", body: JSON.stringify({ label, start_ts, end_ts }) }),
  deleteTest: (id: number) => j<any>(`/api/tests/${id}`, { method: "DELETE" }),
  correlation: (id: number) => j<any>(`/api/tests/${id}/correlation`),
  combined: () => j<any>("/api/tests/combined"),
};

export type StreamEvent =
  | { type: "reading"; endpoint_id: number }
  | { type: "reference"; power: number }
  | { type: "config" };

// useStream subscribes to /api/stream (SSE) and invokes onEvent for each push.
export function useStream(onEvent: (e: StreamEvent) => void) {
  const ref = useRef(onEvent);
  ref.current = onEvent;
  useEffect(() => {
    const es = new EventSource("/api/stream");
    es.onmessage = (m) => {
      try { ref.current(JSON.parse(m.data)); } catch { /* ignore */ }
    };
    es.onerror = () => { /* EventSource auto-reconnects */ };
    return () => es.close();
  }, []);
}

// useAsync runs a loader on mount and whenever `dep` changes, with a manual reload.
export function useAsync<T>(loader: () => Promise<T>, dep: any = null) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const loaderRef = useRef(loader);
  loaderRef.current = loader;
  const reload = () =>
    loaderRef.current().then((d) => { setData(d); setError(null); }).catch((e) => setError(String(e)));
  useEffect(() => { reload(); /* eslint-disable-next-line */ }, [dep]);
  return { data, error, reload };
}
