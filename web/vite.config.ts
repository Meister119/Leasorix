import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// In development the API is proxied to a locally running leasorixd.
const backend = process.env.LEASORIX_URL ?? "https://127.0.0.1:8067";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1024,
  },
  server: {
    proxy: {
      "/api": { target: backend, secure: false, changeOrigin: true },
    },
  },
  test: {
    include: ["src/**/*.test.ts"],
  },
});
