// winnow API client + SSE stream hook (no polling).
import { useEffect, useRef } from "react";

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
  r2: number | null;
  score: number;
  window_delta: number;
  window_packets: number;
  slope: number | null;
  baseline_w: number | null;
  suggested_multiplier: number | null;
  anchor_multiplier: number | null;
  meter_energy_kwh: number | null;
  floor_ok: boolean | null;
  lag_buckets: number | null;
  confidence: number | null;
  confidence_parts?: Record<string, number>;
  pub_multiplier: number;
  pub_unit: string | null;
  is_mine: boolean;
  publish: boolean;
}

export interface TestWindow {
  id: number;
  label: string;
  start_ts: string;
  end_ts: string | null;
  source: string;
  known_load_w: number | null;
  known_entity_id: string | null;
}

export interface SourceHealth {
  source: string; alive: boolean; age_seconds: number | null;
  last_ts: string | null; total_count: number; packets_last_min: number;
}
export interface Health {
  alive: boolean; sources: SourceHealth[]; unique_meters: number; packets_last_min: number;
}

export interface Status {
  ha_ok: boolean; mqtt_ok: boolean;
  monitored_entities: string[]; monitored_floor_w: number; published: Meter[];
}

export interface PowerEntity { entity_id: string; name: string; state: string; unit: string; kind: string; }
export interface UtilityStat { id: string; name: string; unit: string; }
export interface UtilityComparePoint { ts: string; utility_kwh: number; meter_kwh: number | null; coverage_pct: number; }
export interface UtilityDayEstimate { day: string; flat_kwh: number; shaped_kwh: number | null; meter_kwh: number | null; }
export interface UtilityCompare {
  statistic_id: string; period: string; utility_multiplier: number | null;
  buckets_covered: number; buckets: UtilityComparePoint[]; daily_estimate?: UtilityDayEstimate[];
}
export interface UtilitySeriesPoint { ts: string; kwh: number; meter_kwh: number | null; coverage_pct: number; cost: number | null; }
export interface UtilitySeries {
  statistic_id: string; period: string; unit: string; currency: string;
  cost_per_kwh: number; total_kwh: number; bucket_count: number;
  reconcile_meters: number[]; points: UtilitySeriesPoint[];
  daily_estimate?: UtilityDayEstimate[];
}

export interface ScanSettings { freq: string; gain: string; ppm: string; msgtype: string; filterid: string; }
export interface Device extends ScanSettings {
  serial: string; dev_index: number; name: string; tuner: string;
  last_seen: string | null; enabled: boolean; label: string;
  alive: boolean; packets_last_min: number; meters_heard: number; age_seconds: number | null;
}
export interface DeviceConfig { enabled?: boolean; label?: string; freq?: string; gain?: string; ppm?: string; msgtype?: string; filterid?: string; }
export interface DevicesResp { devices: Device[]; defaults: ScanSettings; }

export interface Anomaly { kind: string; endpoint_id?: number; source?: string; detail: string; }
export interface PublishedLive {
  endpoint_id: number; name: string; commodity: string; unit: string;
  multiplier: number; rate: number | null; today: number; cost_today: number;
}
export interface Overview {
  currency: string; cost_per_kwh: number; published: PublishedLive[]; anomalies: Anomaly[];
}
export interface Benchmark {
  endpoint_id: number; commodity: string; days: number;
  yours: number; median: number; percentile: number; peers: number;
}
export interface TableStat { name: string; bytes: number; rows: number; }
export interface SourceStat { source: string; rows: number; bytes: number; oldest?: string; newest?: string; }
export interface DBStats {
  total_bytes: number; tables: TableStat[]; reading_rows: number;
  oldest_reading: string | null; newest_reading: string | null;
  chunks: number; compressed_chunks: number;
  uncompressed_bytes: number; compressed_bytes: number;
  retention_policy?: string; compression_policy?: string;
  sources: SourceStat[];
}
export type MaintOp = "vacuum" | "reindex" | "refresh_agg" | "compress" | "prune_devices" | "scrub_zeros";
export type DeleteMode = "age" | "source" | "all_tests" | "all_readings";

export interface CoverageCell { source: string; endpoint_id: number; packets: number; last_seen: string; }
export interface CoverageSource { source: string; label: string; present: boolean; }
export interface CoverageResp { cells: CoverageCell[]; sources: CoverageSource[]; }

export interface AuthorizedAgent { label: string; pubkey: string; fingerprint: string; }
export interface RemoteDongle { source: string; label: string; alive: boolean; last_seen: string | null; }
export interface PendingAgent { pubkey: string; fingerprint: string; name: string; remote_addr: string; first_seen: string; last_seen: string; }
export interface AgentsResp {
  server_key: string; server_fingerprint: string; tls_fingerprint: string;
  authorized: AuthorizedAgent[]; remotes: RemoteDongle[]; pending: PendingAgent[];
}
export interface ProfilePoint { key: number; value: number; }
export interface HeatCell { dow: number; hour: number; value: number; }
export interface DailyPoint { day: string; value: number; }

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
  deleteMeter: (id: number, purge: boolean) =>
    j<any>(`/api/meters/${id}${purge ? "?purge=true" : ""}`, { method: "DELETE" }),
  filterCmd: (id: number) => j<any>(`/api/meters/${id}/filter-command`),
  profile: (id: number, type: string, days = 14) =>
    j<any>(`/api/meters/${id}/profile?type=${type}&days=${days}`),
  benchmark: (id: number, days = 7) => j<Benchmark>(`/api/meters/${id}/benchmark?days=${days}`),
  series: (ids: number[], qs: string) =>
    j<Record<string, { bucket: string; value: number }[]>>(`/api/series?ids=${ids.join(",")}&${qs}`),

  settings: () => j<Record<string, any>>("/api/settings"),
  putSettings: (body: Record<string, string>) =>
    j<any>("/api/settings", { method: "PUT", body: JSON.stringify(body) }),
  testIntegrations: (body: Record<string, string>) =>
    j<any>("/api/integrations/test", { method: "POST", body: JSON.stringify(body) }),
  powerEntities: () => j<PowerEntity[]>("/api/ha/power-entities"),
  createHelper: (name: string, entities: string[]) =>
    j<any>("/api/ha/create-helper", { method: "POST", body: JSON.stringify({ name, entities }) }),
  utilityStatistics: () => j<UtilityStat[]>("/api/ha/utility-statistics"),
  utilityCompare: (id: number) => j<UtilityCompare>(`/api/meters/${id}/utility-compare`),
  utilitySeries: () => j<UtilitySeries>("/api/utility/series"),

  devices: () => j<DevicesResp>("/api/devices"),
  putDevice: (serial: string, body: DeviceConfig) =>
    j<any>(`/api/devices/${encodeURIComponent(serial)}`, { method: "PUT", body: JSON.stringify(body) }),

  overview: () => j<Overview>("/api/overview"),
  anomalies: () => j<Anomaly[]>("/api/anomalies"),

  adminStats: () => j<DBStats>("/api/admin/stats"),
  maintenance: (op: MaintOp) =>
    j<any>("/api/admin/maintenance", { method: "POST", body: JSON.stringify({ op }) }),
  adminDelete: (body: { mode: DeleteMode; days?: number; source?: string; confirm: string }) =>
    j<{ ok: boolean; mode: string; removed: number }>("/api/admin/delete", { method: "POST", body: JSON.stringify(body) }),

  coverage: () => j<CoverageResp>("/api/diagnostics/coverage"),

  agents: () => j<AgentsResp>("/api/agents"),
  authorizeAgent: (label: string, pubkey: string) =>
    j<{ ok: boolean }>("/api/agents", { method: "POST", body: JSON.stringify({ label, pubkey }) }),
  revokeAgent: (pubkey: string) =>
    j<{ ok: boolean }>("/api/agents/revoke", { method: "POST", body: JSON.stringify({ pubkey }) }),
  sourceTimeline: (qs = "") => j<Record<string, { bucket: string; packets: number }[]>>(`/api/diagnostics/sources${qs}`),

  identify: (hours: number, bucket = "auto", commodity = "electric") =>
    j<any>(`/api/identify?hours=${hours}&bucket=${bucket}&commodity=${commodity}`),
  identifyAuto: () => j<any>("/api/identify/auto"),
  referenceSeries: (startISO: string, endISO: string) =>
    j<{ bucket: string; value: number }[]>(`/api/reference/series?start=${startISO}&end=${endISO}`),

  tests: () => j<TestWindow[]>("/api/tests"),
  startTest: (label: string, known?: { known_load_w?: number; known_entity_id?: string }) =>
    j<TestWindow>("/api/tests/start", { method: "POST", body: JSON.stringify({ label, ...known }) }),
  stopTest: (id: number) => j<TestWindow>(`/api/tests/${id}/stop`, { method: "POST" }),
  createTest: (label: string, start_ts: string, end_ts: string) =>
    j<TestWindow>("/api/tests", { method: "POST", body: JSON.stringify({ label, start_ts, end_ts }) }),
  deleteTest: (id: number) => j<any>(`/api/tests/${id}`, { method: "DELETE" }),
  correlation: (id: number) => j<any>(`/api/tests/${id}/correlation`),
  combined: () => j<any>("/api/tests/combined"),
};

export type StreamEvent =
  | { type: "reading"; endpoint_id: number; source: string }
  | { type: "reference"; power: number }
  | { type: "config" }
  | { type: "agent" };

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
