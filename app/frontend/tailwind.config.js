/** winnow — Pro Observability Dark design tokens. */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // layered backgrounds for real elevation
        app: "#0b0f14",
        sidebar: "#0d1218",
        surface: "#121922",
        raised: "#16202b",
        overlay: "#1b2735",
        // borders
        border: "#1e2a36",
        "border-strong": "#2a3947",
        // text ladder
        text: "#e8eef6",
        secondary: "#9fb0c3",
        tertiary: "#64748b",
        faint: "#465263",
        // identity
        brand: { DEFAULT: "#2dd4bf", dim: "#0f766e", soft: "#0e3b38" },
        gold: { DEFAULT: "#fbbf24", soft: "#3a2f14" },
        // status
        good: "#34d399",
        warn: "#fbbf24",
        bad: "#f87171",
        info: "#60a5fa",
        violet: "#a78bfa",
      },
      fontFamily: {
        sans: ['"Inter Variable"', "ui-sans-serif", "system-ui", "-apple-system", "Segoe UI", "Roboto", "sans-serif"],
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "Consolas", "monospace"],
      },
      fontSize: {
        micro: ["11px", { lineHeight: "16px" }],
        small: ["13px", { lineHeight: "18px" }],
        body: ["14px", { lineHeight: "20px" }],
        h3: ["15px", { lineHeight: "22px", fontWeight: "600" }],
        h2: ["18px", { lineHeight: "26px", fontWeight: "600" }],
        h1: ["22px", { lineHeight: "28px", fontWeight: "600" }],
        display: ["30px", { lineHeight: "36px", fontWeight: "700" }],
        kpi: ["28px", { lineHeight: "32px", fontWeight: "600" }],
      },
      boxShadow: {
        card: "0 1px 2px rgba(0,0,0,0.3)",
        raised: "0 1px 3px rgba(0,0,0,0.45), 0 1px 2px rgba(0,0,0,0.3)",
        overlay: "0 12px 34px rgba(0,0,0,0.55), 0 2px 8px rgba(0,0,0,0.4)",
      },
      keyframes: {
        spin: { to: { transform: "rotate(360deg)" } },
        "fade-in": { from: { opacity: 0, transform: "translateY(4px)" }, to: { opacity: 1, transform: "none" } },
        "slide-in": { from: { opacity: 0, transform: "translateX(8px)" }, to: { opacity: 1, transform: "none" } },
        pulse2: { "0%,100%": { opacity: 1 }, "50%": { opacity: 0.35 } },
        indeterminate: { "0%": { left: "-40%", width: "40%" }, "100%": { left: "100%", width: "40%" } },
      },
      animation: {
        "fade-in": "fade-in 0.16s ease-out",
        "slide-in": "slide-in 0.18s ease-out",
        pulse2: "pulse2 1.6s ease-in-out infinite",
        indeterminate: "indeterminate 1.1s ease-in-out infinite",
      },
    },
  },
  plugins: [],
};
