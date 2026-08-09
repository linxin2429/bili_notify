import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { configuredWorkerCount } from './test-workers.ts'

const maxWorkers = configuredWorkerCount()

export default defineConfig({
  plugins: [react()],
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
    // Self-hosted CI runners are slower for MUI + userEvent interactions;
    // several page tests need more than the default 5s.
    testTimeout: 15_000,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'lcov'],
      reportsDirectory: 'coverage',
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/**/*.test.{ts,tsx}', 'src/main.tsx', 'src/types.ts', 'src/test/**', 'src/test-setup.ts'],
      thresholds: {
        statements: 80,
        branches: 80,
        functions: 80,
        lines: 80,
        'src/pages/SettingsPage.tsx': { statements: 90, branches: 90, functions: 90, lines: 90 },
        'src/app/Console.tsx': { statements: 75, branches: 80, functions: 65, lines: 90 },
        'src/pages/AuditLogsPage.tsx': { statements: 85, branches: 65, functions: 70, lines: 95 },
      },
    },
  },
})
