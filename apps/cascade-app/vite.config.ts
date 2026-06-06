/// <reference types="vitest" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "path";

// Tauri expects Vite to listen on port 1420 during dev.
// TAURI_DEV_HOST is set by `pnpm tauri dev` when running on a device or docker.
const host = process.env.TAURI_DEV_HOST;

export default defineConfig({
  plugins: [react()],

  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },

  // Prevent Vite from obscuring Rust errors
  clearScreen: false,

  server: {
    port: 1420,
    strictPort: true,
    host: host ?? false,
    hmr: host
      ? { protocol: "ws", host, port: 1421 }
      : undefined,
    watch: {
      // Tell Vite to ignore watching `src-tauri`
      ignored: ["**/src-tauri/**"],
    },
  },

  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: [],
  },

  build: {
    // Tauri platform target
    target:
      process.env.TAURI_ENV_PLATFORM === "windows"
        ? "chrome105"
        : process.env.TAURI_ENV_PLATFORM === "macos" ||
          process.env.TAURI_ENV_PLATFORM === "ios"
        ? "safari13"
        : "chrome105",
    minify: process.env.TAURI_ENV_DEBUG === "true" ? false : ("esbuild" as const),
    sourcemap: process.env.TAURI_ENV_DEBUG === "true",
  },
});
