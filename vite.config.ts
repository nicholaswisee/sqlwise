import { defineConfig } from "vite-plus";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  input: "web/src/main.ts",
  plugins: [tailwindcss()],
  publicDir: "web/public",
  server: {
    port: 5173,
    strictPort: true,
    origin: "http://localhost:5173",
    cors: { origin: "http://localhost:8080" },
  },
  build: {
    outDir: "web/dist",
    emptyOutDir: true,
    manifest: true,
  },
  lint: {
    options: { typeAware: true, typeCheck: true },
  },
});
