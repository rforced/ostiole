import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  // The tests share one server and walk it from fresh router to configured.
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: 'http://127.0.0.1:18090',
    trace: 'retain-on-failure',
    // The browser reads times in its own zone, so a spec that asserts
    // "4:00:00" for a cron at 04:00 passes in CI and fails on a
    // workstation four hours behind it. Pin the zone rather than write
    // every such assertion twice.
    timezoneId: 'UTC',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] }, testIgnore: '24-mobile.spec.js' },
    // The phone layout, last, against the router the desktop specs configured.
    {
      name: 'mobile',
      use: { ...devices['Pixel 7'] },
      testMatch: '24-mobile.spec.js',
      dependencies: ['chromium'],
    },
  ],
  webServer: {
    command: 'sh e2e/serve.sh',
    url: 'http://127.0.0.1:18090/api/v1/health',
    timeout: 120_000,
    reuseExistingServer: false,
    stdout: 'ignore',
    stderr: 'pipe',
  },
})
