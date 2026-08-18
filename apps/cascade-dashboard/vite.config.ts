import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import path from 'path'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 9762,
    proxy: {
      '/api': {
        target: 'http://localhost:9761',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    // T-P7-E17-02: split heavy vendor deps into their own chunks so the initial
    // entry stays small. Ordering matters — specific react-* packages are
    // matched before the generic `react` catch-all.
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('react-router')) return 'vendor-router'
          if (id.includes('@radix-ui')) return 'vendor-radix'
          if (id.includes('recharts') || id.includes('d3-') || id.includes('/d3/') || id.includes('victory-vendor')) {
            return 'vendor-charts'
          }
          if (id.includes('highlight.js')) return 'vendor-highlight'
          if (
            id.includes('react-markdown') ||
            id.includes('remark') ||
            id.includes('rehype') ||
            id.includes('unified') ||
            id.includes('micromark') ||
            id.includes('mdast') ||
            id.includes('hast')
          ) {
            return 'vendor-markdown'
          }
          if (id.includes('lucide-react')) return 'vendor-icons'
          if (id.includes('date-fns')) return 'vendor-date'
          // Generic react catch-all — must be last so react-* packages above win.
          if (id.includes('react') || id.includes('scheduler')) return 'vendor-react'
        },
      },
    },
  },
  base: './',
  test: {
    environment: 'jsdom',
    globals: true,
  },
})
