/** winnow — dual light/dark theme. Colors are semantic tokens backed by CSS
 *  variables (RGB channels) defined in index.css for :root (light) and .dark,
 *  wired as rgb(var(--x) / <alpha-value>) so opacity modifiers still work. */
const tok = (name) => `rgb(var(--${name}) / <alpha-value>)`;

export default {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // layered surfaces (elevation)
        app: tok("app"),
        sidebar: tok("sidebar"),
        surface: tok("surface"),
        raised: tok("raised"),
        overlay: tok("overlay"),
        // borders
        border: tok("border"),
        "border-strong": tok("border-strong"),
        // text ladder
        text: tok("text"),
        secondary: tok("secondary"),
        tertiary: tok("tertiary"),
        faint: tok("faint"),
        // identity + the foreground that contrasts each solid accent
        brand: tok("brand"),
        "on-brand": tok("on-brand"),
        gold: tok("gold"),
        "on-gold": tok("on-gold"),
        // status
        good: tok("good"),
        warn: tok("warn"),
        bad: tok("bad"),
        info: tok("info"),
        // categorical chart palette
        "chart-1": tok("chart-1"),
        "chart-2": tok("chart-2"),
        "chart-3": tok("chart-3"),
        "chart-4": tok("chart-4"),
        "chart-5": tok("chart-5"),
        "chart-6": tok("chart-6"),
      },
      fontFamily: {
        sans: ['"Hanken Grotesk Variable"', "ui-sans-serif", "system-ui", "-apple-system", "Segoe UI", "Roboto", "sans-serif"],
        mono: ['"JetBrains Mono Variable"', "ui-monospace", "SFMono-Regular", "Menlo", "Consolas", "monospace"],
      },
      fontSize: {
        micro: ["11px", { lineHeight: "16px" }],
        small: ["13px", { lineHeight: "18px" }],
        body: ["14px", { lineHeight: "20px" }],
        h3: ["15px", { lineHeight: "22px", fontWeight: "600" }],
        h2: ["18px", { lineHeight: "26px", fontWeight: "600" }],
        h1: ["22px", { lineHeight: "28px", fontWeight: "650" }],
        display: ["30px", { lineHeight: "36px", fontWeight: "700" }],
        kpi: ["28px", { lineHeight: "32px", fontWeight: "650" }],
      },
      boxShadow: {
        card: "var(--shadow-card)",
        raised: "var(--shadow-raised)",
        overlay: "var(--shadow-overlay)",
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
