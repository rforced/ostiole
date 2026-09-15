import { expect, test } from '@playwright/test'

import { login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('the dashboard summarises interfaces, rules, services, and warnings', async ({ page }) => {
  await login(page)
  await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible()

  // The LAN the wizard configured, with the address it should carry.
  const interfaces = page.getByRole('region', { name: 'Interfaces' })
  await expect(interfaces).toContainText('192.168.50.1/24')
  await expect(interfaces).toContainText('lan')
  await expect(interfaces).toContainText('wan')

  // Counters come from the kernel (the stub nft here).
  const rules = page.getByRole('region', { name: 'Busiest rules' })
  await expect(rules).toContainText('Allow LAN to any')
  await expect(rules).toContainText('42')
  await expect(rules).toContainText('Blocked by the default policy: 9 packets')

  // DHCP and DNS as the services test left them.
  const services = page.getByRole('region', { name: 'Services' })
  await expect(services).toContainText('of 100')
  await expect(services).toContainText('lan')

  // The stub reports a table Ostiole does not own.
  const foreign = page.locator('[data-warning="foreign-tables"]')
  await expect(foreign).toContainText('ip nat')

  await page.screenshot({ path: shot('50-dashboard'), fullPage: true })
})

test('the dashboard refreshes on demand', async ({ page }) => {
  await login(page)
  const overview = page.waitForResponse((r) => r.url().includes('/api/v1/overview'))
  await page.getByRole('button', { name: 'Refresh' }).click()
  expect((await overview).status()).toBe(200)
})
