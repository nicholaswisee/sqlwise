import { fileURLToPath } from "node:url";
import { defineConfig } from "vite-plus";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  root: fileURLToPath(new URL(".", import.meta.url)),
  plugins: [tailwindcss(), tanstackStart({ spa: { enabled: true } }), react()],
  // Keep the prerender listener and fetch address consistent inside containers.
  preview: { host: "127.0.0.1" },
  server: {
    port: 5174,
    strictPort: true,
    proxy: {
      "/api/v1": { target: "http://localhost:8080" },
      "/health": { target: "http://localhost:8080" },
    },
  },
});
