import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

const outDir = path.resolve(import.meta.dirname, '../internal/webui/dist')
const backend = 'http://127.0.0.1:8080'

// Keeps `go:embed all:dist` satisfiable and `git status` clean after emptyOutDir.
const keepDist = {
  name: 'keep-dist',
  closeBundle() {
    mkdirSync(outDir, { recursive: true })
    writeFileSync(path.join(outDir, '.gitkeep'), '')
  },
}

export default defineConfig({
  plugins: [react(), tailwindcss(), keepDist],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
  build: { outDir, emptyOutDir: true },
  server: {
    proxy: Object.fromEntries(['/v1', '/auth', '/healthz', '/readyz'].map((route) => [route, backend])),
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    testTimeout: 15000,
  },
})
