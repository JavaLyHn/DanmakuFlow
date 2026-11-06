import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: Boolean(process.env.CI),
  timeout: 15_000,
  globalTimeout: 120_000,
  expect: { timeout: 5_000 },
  reporter: [['line'], ['html', { open: 'never' }]],
  outputDir: 'test-results',
  use: {
    browserName: 'chromium',
    baseURL: 'http://127.0.0.1:4173',
    viewport: { width: 1280, height: 900 },
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  webServer: {
    command: 'npm run preview -- --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: false,
    timeout: 30_000,
  },
})
