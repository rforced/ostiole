import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('enable DHCP with a scope and DNS with an override, then apply', async ({ page }) => {
  await login(page)
  await page.goto('/services')
  await expect(page.getByRole('heading', { name: 'Services' })).toBeVisible()

  // The wizard enabled DHCP on the LAN with a pool of hosts 100-199; edit that scope.
  await expect(page.getByLabel('DHCP server enabled')).toBeChecked()
  const scopeRow = page.getByRole('row').filter({ hasText: '192.168.50.100' })
  await scopeRow.getByRole('button', { name: 'Edit' }).click()
  let dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Range end')).toHaveValue('192.168.50.199')
  await dialog.getByLabel('Lease time').fill('1d')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(scopeRow).toContainText('1d')

  // A second scope offers only interfaces without one; the LAN is excluded.
  await page.getByRole('button', { name: 'Add scope' }).click()
  dialog = page.getByRole('dialog')
  await expect(dialog.locator('#sc-if option', { hasText: '192.168.50.1/24' })).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  await page.getByRole('button', { name: 'Add static lease' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('MAC address').fill('AA:BB:CC:DD:EE:01')
  await dialog.getByLabel('IPv4 address').fill('192.168.50.20')
  await dialog.getByLabel('Hostname').fill('nas')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'aa:bb:cc:dd:ee:01' })).toContainText('nas')
  await page.screenshot({ path: shot('40-services-dhcp'), fullPage: true })

  await page.getByRole('tab', { name: 'DNS' }).click()
  await expect(page.getByLabel('DNS service enabled')).toBeChecked()
  await page.getByLabel('Upstream resolvers').fill('1.1.1.1, 9.9.9.9')
  await page.getByLabel('Local domain').fill('lan')
  await page.getByRole('button', { name: 'Add host' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Hostname').fill('printer')
  await dialog.getByLabel('IP address').fill('192.168.50.30')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'printer' })).toContainText('192.168.50.30')
  await page.screenshot({ path: shot('41-services-dns'), fullPage: true })

  await applyAndConfirm(page)

  await page.reload()
  await expect(page.getByLabel('DHCP server enabled')).toBeChecked()
  await page.getByRole('tab', { name: 'DNS' }).click()
  await expect(page.getByLabel('Upstream resolvers')).toHaveValue('1.1.1.1, 9.9.9.9')
  await page.getByRole('tab', { name: 'Leases' }).click()
  await expect(page.getByText('No leases yet.')).toBeVisible()
})

test('a scope outside the interface subnet is rejected by the server', async ({ page }) => {
  await login(page)
  await page.goto('/services')
  await page
    .getByRole('row')
    .filter({ hasText: '192.168.50.100' })
    .getByRole('button', { name: 'Edit' })
    .click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Range end').fill('10.9.9.9')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  await expect(page.getByRole('alert')).toContainText('rangeEnd')
  await page.getByRole('button', { name: 'Discard' }).click()
})
