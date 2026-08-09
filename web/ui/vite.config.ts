import { defineConfig } from 'vitest/config'
import react, { reactCompilerPreset } from '@vitejs/plugin-react'
import babel from '@rolldown/plugin-babel'
import { configuredWorkerCount } from './test-workers.ts'

const maxWorkers = configuredWorkerCount()

export default defineConfig(({ mode }) => ({
  // Coverage measures authored behavior. React Compiler rewrites each component
  // into generated memo-cache control flow, which V8 maps back to the source and
  // would make compiler internals look like untested application branches.
  plugins: [react(), ...(mode === 'test' ? [] : [babel({ presets: [reactCompilerPreset()] })])],
  build: {
    outDir: '../dist',
    emptyOutDir: true,
    manifest: true,
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
      exclude: ['src/**/*.test.{ts,tsx}', 'src/main.tsx', 'src/test/**', 'src/test-setup.ts'],
      thresholds: { statements: 80, branches: 80, functions: 80, lines: 80 },
    },
  },
}))
