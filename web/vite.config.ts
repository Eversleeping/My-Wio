/// <reference types="vitest/config" />
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { VitePWA } from "vite-plugin-pwa";

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
  build: { target: "es2022", sourcemap: true }
});
