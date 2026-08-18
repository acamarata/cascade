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
    // Include src unit tests + e2e integration tests; exclude binary-requiring .e2e.ts files
    include: ["src/**/*.{test,spec}.{ts,tsx}", "e2e/**/*.integration.test.{ts,tsx}"],
    exclude: ["e2e/**/*.e2e.ts", "e2e/wdio.conf.ts"],
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
    // T-P7-E17-01: split heavy vendor deps into their own chunks so the initial
    // entry stays small. Ordering matters — specific react-* packages are
    // matched before the generic `react` catch-all. highlight.js language modes
    // are already auto-split by highlight.js's own dynamic imports (the small
    // per-language chunks in the build output); the chunk below is only the
    // highlight.js core. CodeMirror language modes (@codemirror/language-data)
    // are likewise lazy-loaded by CodeMirror, so the oversized baseline chunk
    // was dominated by eagerly-bundled vendors + all route code, not language
    // modes — verified against the real build output.
    // Note: vendor-state (zustand/immer/zod) is merged into vendor-react to
    // avoid a circular chunk dependency (zustand → use-sync-external-store →
    // react → back into state via shared deps). The combined chunk is ~220 kB,
    // well under the 500 kB threshold.
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) return
          if (id.includes("@tauri-apps")) return "vendor-tauri"
          if (id.includes("react-router")) return "vendor-router"
          if (id.includes("@radix-ui")) return "vendor-radix"
          if (id.includes("@xyflow")) return "vendor-flow"
          // CodeMirror — split into sub-chunks. Check language-data before
          // language (language-data contains "language" as a substring).
          // These are lazy-loaded only when the vault editor opens.
          if (id.includes("@codemirror/language-data") || id.includes("@codemirror/lang-markdown")) {
            return "vendor-codemirror-lang"
          }
          if (id.includes("@codemirror/view")) return "vendor-codemirror-view"
          if (
            id.includes("@codemirror/state") ||
            id.includes("@codemirror/language") ||
            id.includes("@codemirror/commands")
          ) {
            return "vendor-codemirror-core"
          }
          if (
            id.includes("@codemirror/autocomplete") ||
            id.includes("@codemirror/search") ||
            id.includes("@codemirror/theme-one-dark") ||
            id.includes("@uiw/react-codemirror")
          ) {
            return "vendor-codemirror-ext"
          }
          if (id.includes("recharts") || id.includes("d3-") || id.includes("/d3/") || id.includes("victory-vendor")) {
            return "vendor-charts"
          }
          if (id.includes("highlight.js")) return "vendor-highlight"
          if (
            id.includes("react-markdown") ||
            id.includes("remark") ||
            id.includes("rehype") ||
            id.includes("unified") ||
            id.includes("micromark") ||
            id.includes("mdast") ||
            id.includes("hast") ||
            id.includes("gray-matter")
          ) {
            return "vendor-markdown"
          }
          if (id.includes("lucide-react")) return "vendor-icons"
          if (id.includes("cmdk")) return "vendor-cmdk"
          // Generic react + state catch-all — must be last so react-* packages
          // above win. zustand/immer/zod are included here to avoid a circular
          // chunk dependency with the react chunk.
          if (
            id.includes("react") ||
            id.includes("scheduler") ||
            id.includes("zustand") ||
            id.includes("immer") ||
            id.includes("zod")
          ) {
            return "vendor-react"
          }
        },
      },
    },
  },
});
