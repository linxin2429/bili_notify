import { defineConfig, devices } from '@playwright/test'
import { configuredWorkerCount } from './test-workers.ts'

const workers = configuredWorkerCount() ?? 1

export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  fullyParallel: true,
  workers,
  timeout: 120_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [['line'], ['html', { open: 'never' }]] : 'list',
  use: {
    ignoreHTTPSErrors: true,
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
  },
  projects: [
    { name: 'desktop-light', use: { viewport: { width: 1440, height: 1000 }, colorScheme: 'light' } },
    { name: 'mobile-dark', use: { ...devices['Pixel 7'], colorScheme: 'dark' } },
  ],
})
