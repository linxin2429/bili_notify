import { defineConfig } from 'vitest/config'
import react, { reactCompilerPreset } from '@vitejs/plugin-react'
import babel from '@rolldown/plugin-babel'
import { configuredWorkerCount } from './test-workers.ts'

const maxWorkers = configuredWorkerCount()

export default defineConfig({
  plugins: [react(), babel({ presets: [reactCompilerPreset()] })],
  build: {
    outDir: '../dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 600,
  },
  test: {
    maxWorkers,
    environment: 'jsdom',
    setupFiles: './src/test-setup.ts',
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
    testTimeout: 15_000,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'lcov'],
      reportsDirectory: 'coverage',
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/**/*.test.{ts,tsx}', 'src/main.tsx', 'src/types.ts', 'src/test/**', 'src/test-setup.ts'],
    },
  },
})
