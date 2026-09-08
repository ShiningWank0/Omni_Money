import { defineConfig, devices } from '@playwright/test'

// The server/browser E2E suite only runs against a disposable server started
// by tests/e2e/run-server-e2e.sh. Without E2E_BASE_URL every test is skipped
// so a stray `npm test`/`npx playwright test` can never hit a real deployment.
const baseURL = process.env.E2E_BASE_URL

export default defineConfig({
  testDir: 'tests/e2e',
  timeout: 120_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  use: {
    baseURL,
    headless: true,
    screenshot: 'off',
    video: 'off',
    trace: 'off',
    locale: 'ja-JP',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
