import { defineConfig } from '@playwright/test'

// Public license UI only: no backend, database, credentials, or user vault.
export default defineConfig({
  testDir: 'tests/licenses-ui',
  outputDir: `../.oss-cache/browser-tests/${process.env.OSS_TEST_TARGET || 'default'}`,
  workers: 1,
  retries: 0,
  use: { baseURL: 'http://127.0.0.1:4173', headless: true, screenshot: 'only-on-failure', locale: 'ja-JP' },
  webServer: { command: 'npm run preview -- --host 127.0.0.1 --port 4173', url: 'http://127.0.0.1:4173/login', reuseExistingServer: false },
})
