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

// human-readable byte size
export const bytes = (n: number | null | undefined): string => {
  if (n === null || n === undefined) return "–";
  if (n < 1024) return `${n} B`;
  const u = ["KB", "MB", "GB", "TB"];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${u[i]}`;
};

// Copy text to the clipboard, with a fallback for non-secure contexts. The
// dashboard is typically served over plain HTTP on a LAN, where
// navigator.clipboard is undefined — so fall back to a hidden textarea +
// execCommand. Rejects if neither path works (caller can toast the failure).
export const copyText = (value: string): Promise<void> => {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(value);
  return new Promise((resolve, reject) => {
    try {
      const ta = document.createElement("textarea");
      ta.value = value;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      const ok = document.execCommand("copy");
      document.body.removeChild(ta);
      ok ? resolve() : reject(new Error("copy command failed"));
    } catch (e) {
      reject(e);
    }
  });
};

// compact span between two ISO timestamps, e.g. "12d 4h"
export const spanOf = (a: string | null, b: string | null): string => {
  if (!a || !b) return "–";
  const ms = new Date(b).getTime() - new Date(a).getTime();
  if (!(ms > 0)) return "–";
  const d = Math.floor(ms / 86400000), h = Math.floor((ms % 86400000) / 3600000);
  if (d > 0) return `${d}d ${h}h`;
  const m = Math.floor((ms % 3600000) / 60000);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
};
