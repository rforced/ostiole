import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  // The tests of a file run in order, on a server of their own started from
  // the router global-setup.js configured (e2e/fixtures.js), so the files
  // run side by side.
  fullyParallel: false,
  // CI's runner has four vCPUs. Held to four CPUs here, four workers took
  // 2.0 min and two 3.4. Elsewhere, half the cores.
  workers: process.env.CI ? 4 : undefined,
  retries: 0,
  globalSetup: './e2e/global-setup.js',
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'list',
  use: {
    trace: 'retain-on-failure',
    // The browser reads times in its own zone, so a spec that asserts
    // "4:00:00" for a cron at 04:00 passes in CI and fails on a
    // workstation four hours behind it. Pin the zone rather than write
    // every such assertion twice.
    timezoneId: 'UTC',
  },
  projects: [
    // The phone layout. First, so the longest file starts first.
    { name: 'mobile', use: { ...devices['Pixel 7'] }, testMatch: '24-mobile.spec.js' },
    { name: 'chromium', use: { ...devices['Desktop Chrome'] }, testIgnore: '24-mobile.spec.js' },
  ],
})
