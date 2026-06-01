// useFetch: load on mount + when deps change (user actions / config events).
// No timers, no polling. Tracks a global in-flight count for the top loading bar.
import { useEffect, useRef, useState, useSyncExternalStore } from "react";

let inflight = 0;
const subs = new Set<() => void>();
function bump(n: number) { inflight += n; subs.forEach((f) => f()); }

export function useFetch<T>(loader: () => Promise<T>, deps: any[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const ref = useRef(loader);
  ref.current = loader;
  const reload = () => {
    setLoading(true); bump(1);
    return ref.current()
      .then((d) => { setData(d); setError(null); })
      .catch((e) => setError(String(e)))
      .finally(() => { setLoading(false); bump(-1); });
  };
  useEffect(() => { reload(); /* eslint-disable-next-line */ }, deps);
  return { data, error, loading, reload };
}

export function TopLoadingBar() {
  const on = useSyncExternalStore((f) => { subs.add(f); return () => subs.delete(f); }, () => inflight > 0, () => false);
  if (!on) return null;
  return (
    <div className="pointer-events-none fixed inset-x-0 top-0 z-[60] h-0.5 overflow-hidden">
      <div className="absolute h-full bg-brand animate-indeterminate" />
    </div>
  );
}
