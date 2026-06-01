/** winnow dark design tokens. */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        bg: "#0a0e13",
        surface: "#111923",
        surface2: "#18222f",
        elevated: "#1d2937",
        border: "#26323f",
        borderlt: "#324150",
        text: "#e7edf4",
        muted: "#8d9aac",
        faint: "#5f6c7c",
        brand: { DEFAULT: "#2dd4bf", dim: "#0f766e", soft: "#134e4a" },
        gold: { DEFAULT: "#fbbf24", soft: "#3b2f12" },
        good: "#4ade80",
        bad: "#f87171",
        info: "#60a5fa",
        violet: "#a78bfa",
      },
      fontFamily: {
        sans: ['ui-sans-serif', 'system-ui', '-apple-system', 'Segoe UI', 'Roboto', 'Helvetica', 'Arial', 'sans-serif'],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'monospace'],
      },
      boxShadow: {
        card: "0 1px 2px rgba(0,0,0,0.3), 0 1px 1px rgba(0,0,0,0.2)",
        pop: "0 10px 30px rgba(0,0,0,0.5)",
      },
      keyframes: {
        spin: { to: { transform: "rotate(360deg)" } },
        "fade-in": { from: { opacity: 0, transform: "translateY(4px)" }, to: { opacity: 1, transform: "none" } },
        pulse2: { "0%,100%": { opacity: 1 }, "50%": { opacity: 0.4 } },
      },
      animation: {
        "fade-in": "fade-in 0.15s ease-out",
        pulse2: "pulse2 1.6s ease-in-out infinite",
      },
    },
  },
  plugins: [],
};
