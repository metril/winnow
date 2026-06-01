// Theme-aware Recharts styling. Recharts needs literal colors (it sets SVG
// attributes, where CSS var() doesn't resolve), so we read the resolved CSS-var
// palette via getComputedStyle and recompute whenever the theme flips.
import { useMemo } from "react";
import { useTheme } from "../theme";

function readVar(name: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v ? `rgb(${v})` : "#888";
}
function readChannels(name: string): [number, number, number] {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const p = v.split(/\s+/).map(Number);
  return [p[0] || 0, p[1] || 0, p[2] || 0];
}

export interface ChartTheme {
  grid: string; axis: string; brand: string; gold: string; text: string; faint: string;
  palette: string[];
  heat: (a: number) => string; // sequential ramp for heatmaps/matrices
  empty: string;               // empty heat cell
  axisX: any; axisY: any; gridProps: any; tooltipStyle: any;
}

export function useChartTheme(): ChartTheme {
  const { isDark } = useTheme();
  return useMemo<ChartTheme>(() => {
    const grid = readVar("--border");
    const axis = readVar("--tertiary");
    const brand = readVar("--brand");
    const gold = readVar("--gold");
    const text = readVar("--text");
    const faint = readVar("--faint");
    const [hr, hg, hb] = readChannels("--brand");
    const [er, eg, eb] = readChannels("--raised");
    const palette = [1, 2, 3, 4, 5, 6].map((i) => readVar(`--chart-${i}`));
    const axisX = { stroke: grid, tick: { fill: axis, fontSize: 11 }, tickLine: false, axisLine: false as const };
    const axisY = { ...axisX, width: 48 };
    const gridProps = { horizontal: true, vertical: false, stroke: grid, strokeOpacity: 0.7, strokeDasharray: "3 3" };
    const tooltipStyle = {
      background: readVar("--overlay"),
      border: `1px solid ${readVar("--border-strong")}`,
      borderRadius: 8, fontSize: 12, color: text, boxShadow: "var(--shadow-overlay)",
    };
    return {
      grid, axis, brand, gold, text, faint, palette,
      heat: (a: number) => `rgb(${hr} ${hg} ${hb} / ${Math.max(0, Math.min(1, a))})`,
      empty: `rgb(${er} ${eg} ${eb} / 0.5)`,
      axisX, axisY, gridProps, tooltipStyle,
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isDark]);
}
