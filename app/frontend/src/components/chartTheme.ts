// Shared Recharts theming so every chart speaks one visual language.
export const CHART = {
  grid: "#1e2a36",
  axis: "#64748b",
  brand: "#2dd4bf",
  gold: "#fbbf24",
  palette: ["#2dd4bf", "#fbbf24", "#a78bfa", "#60a5fa", "#34d399", "#f87171", "#fb923c", "#22d3ee"],
};

export const axisX = {
  stroke: CHART.grid,
  tick: { fill: CHART.axis, fontSize: 11 },
  tickLine: false,
  axisLine: false as const,
};
export const axisY = { ...axisX, width: 48 };

export const gridProps = {
  horizontal: true,
  vertical: false,
  stroke: CHART.grid,
  strokeDasharray: "3 3",
};

export const tooltipStyle = {
  background: "#1b2735",
  border: "1px solid #2a3947",
  borderRadius: 10,
  fontSize: 12,
  boxShadow: "0 12px 34px rgba(0,0,0,0.55)",
};
