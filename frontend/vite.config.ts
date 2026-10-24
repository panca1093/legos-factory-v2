import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: process.env.FACTORY_API_URL || "http://127.0.0.1:8080",
        changeOrigin: true,
      },
      "/auth": {
        target: process.env.FACTORY_API_URL || "http://127.0.0.1:8080",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
  },
  test: {
    environment: "jsdom",
    include: ["tests/unit/**/*.spec.ts"],
  },
});
