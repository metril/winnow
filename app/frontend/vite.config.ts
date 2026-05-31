import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Build to a relative-path bundle so FastAPI can serve it from any mount point.
// In dev, proxy /api to the FastAPI server.
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: { outDir: "dist" },
  server: {
    port: 5173,
    proxy: { "/api": "http://localhost:8000" },
  },
});
