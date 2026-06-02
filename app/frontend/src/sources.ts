import { useEffect, useState } from "react";
import { api } from "./api";
import { useLive } from "./live";

// useSourceLabels resolves a dongle source id (serial / dev index) to its
// friendly label (config label → hardware name → the raw id). Fetched once per
// config change so renamed dongles update without polling.
export function useSourceLabels(): (source: string) => string {
  const { configVersion } = useLive();
  const [map, setMap] = useState<Record<string, string>>({});
  useEffect(() => {
    let live = true;
    api.devices().then((d) => {
      if (!live) return;
      const m: Record<string, string> = {};
      d.devices.forEach((dev) => { m[dev.serial] = dev.label || dev.name || dev.serial; });
      setMap(m);
    }).catch(() => {});
    return () => { live = false; };
  }, [configVersion]);
  return (source: string) => map[source] || source;
}
