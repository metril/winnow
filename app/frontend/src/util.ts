export const fmt = (n: number | null | undefined, d = 0): string =>
  n === null || n === undefined ? "–" : n.toLocaleString(undefined, { maximumFractionDigits: d });

export const shortTs = (ts: string | null): string =>
  !ts ? "–" : ts.replace("T", " ").replace(/(\.\d+)?(\+00:00|Z)$/, "").slice(0, 19);

// epoch ms for a chart axis (Recharts handles numeric domains cleanly)
export const tsMs = (ts: string): number => new Date(ts).getTime();

// Convert a <input type=datetime-local> value (local time, no zone) to ISO UTC.
export const localToIso = (v: string): string => (v ? new Date(v).toISOString() : "");

// ISO -> value for datetime-local (local wall clock)
export const isoToLocal = (iso: string): string => {
  const d = new Date(iso);
  const off = d.getTimezoneOffset() * 60000;
  return new Date(d.getTime() - off).toISOString().slice(0, 16);
};

export const since = (hours: number): string =>
  new Date(Date.now() - hours * 3600_000).toISOString();
