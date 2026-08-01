/// <reference types="vitest/config" />
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { VitePWA } from "vite-plugin-pwa";

/**
 * Keep the largest shared runtime dependencies in stable, cacheable chunks.
 * Dependencies of a matched package stay with that package under Rollup's
 * default manual-chunk behaviour, while React is deliberately isolated so
 * the markdown chunk cannot pull a second copy of the runtime into its graph.
 */
export function manualChunks(id: string): string | undefined {
  const normalizedId = id.replaceAll("\\", "/");
  const isPackage = (packageName: string) => normalizedId.includes(`/node_modules/${packageName}/`);

  if (isPackage("react") || isPackage("react-dom") || isPackage("scheduler")) {
    return "vendor-react";
  }

  if (isPackage("react-markdown") || isPackage("remark-gfm")) {
    return "vendor-markdown";
  }

  if (isPackage("lucide-react")) {
    return "vendor-icons";
  }

  return undefined;
}

export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      filename: "sw-v2.js",
      registerType: "autoUpdate",
      includeAssets: ["icon.svg"],
      manifest: {
        name: "Wio 服务器控制台",
        short_name: "Wio",
        description: "自托管服务器、部署与 Codex 运维控制台",
        lang: "zh-CN",
        theme_color: "#171a1f",
        background_color: "#f5f6f4",
        display: "standalone",
        start_url: "/",
        icons: [{ src: "/icon.svg", sizes: "any", type: "image/svg+xml", purpose: "any maskable" }]
      }
    })
  ],
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", ws: true }
    }
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: false
  },
  build: {
    target: "es2022",
    sourcemap: true,
    rollupOptions: {
      output: {
        manualChunks
      }
    }
  }
});
