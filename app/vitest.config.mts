import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// One run covers the renderer (skeleton + features, jsdom) and the Electron
// main-process modules (node).
export default defineConfig({
  plugins: [react()],
  test: {
    // Generous: many agents run suites at once, and a slow box must not fail a test.
    testTimeout: 30_000,
    // Several agents run suites at once; one per core each pushed load past 20 on 10 cores.
    maxWorkers: 3,
    projects: [
      {
        extends: true,
        test: {
          name: 'renderer',
          environment: 'jsdom',
          include: ['src/**/*.test.{ts,tsx}', 'test/**/*.test.{ts,tsx}'],
          setupFiles: ['./test/setup.ts'],
        },
      },
      {
        extends: true,
        test: { name: 'electron', environment: 'node', include: ['electron/**/*.test.ts'] },
      },
    ],
  },
})
