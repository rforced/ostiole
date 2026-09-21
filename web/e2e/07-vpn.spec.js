import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('create a WireGuard tunnel with a peer and apply it', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/wireguard')
  await expect(page.getByRole('heading', { name: 'WireGuard', level: 1 })).toBeVisible()

  await page.getByRole('button', { name: 'Add tunnel' }).click()
  let dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Interface name')).toHaveValue('wg0')
  // The server mints the key pair; the public one is shown to hand out.
  const publicKey = dialog.getByLabel('Public key')
  await expect(publicKey).not.toHaveValue('')
  const tunnelKey = await publicKey.inputValue()
  await dialog.getByLabel('Description').fill('Road warriors')
  await dialog.getByLabel('Address inside the tunnel').fill('10.66.0.1/24')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const tunnel = page.getByRole('region', { name: 'wg0' })
  await expect(tunnel).toContainText('Road warriors')
  await expect(tunnel).toContainText(tunnelKey)
  await expect(tunnel).toContainText('udp/51820')

  await tunnel.getByRole('button', { name: 'Add peer' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('laptop')
  await dialog.getByLabel('Public key').fill('xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=')
  // The dialog suggests the next free address inside the tunnel.
  await expect(dialog.getByLabel('Allowed addresses')).toHaveValue('10.66.0.2/32')
  await dialog.getByRole('button', { name: 'Generate preshared key' }).click()
  await expect(dialog.getByLabel('Preshared key')).not.toHaveValue('')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const peer = page.getByRole('row').filter({ hasText: 'laptop' })
  await expect(peer).toContainText('10.66.0.2/32')
  await expect(peer).toContainText('PSK')
  await page.screenshot({ path: shot('60-vpn'), fullPage: true })

  await applyAndConfirm(page)

  // The tunnel is a real interface: it shows up with its zone and address.
  await page.reload()
  await expect(page.getByRole('region', { name: 'wg0' })).toContainText('10.66.0.1/24')
  await page.goto('/system/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  await expect(page.locator('pre')).toContainText('service:wireguard')
})

test('a bad peer key is rejected by the server', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/wireguard')
  await page.getByRole('region', { name: 'wg0' }).getByRole('button', { name: 'Add peer' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('broken')
  await dialog.getByLabel('Public key').fill('not-a-key')
  await dialog.getByLabel('Allowed addresses').fill('10.66.0.9/32')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  await expect(page.getByRole('alert')).toContainText('publicKey')
  await page.getByRole('button', { name: 'Discard' }).click()
})

test('add a gateway for failover', async ({ page }) => {
  await login(page)
  await page.goto('/routing')
  await page.getByRole('button', { name: 'Add gateway' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('wan1')
  await dialog.getByLabel('Description').fill('Fibre')
  await dialog.getByLabel('Monitor address').fill('9.9.9.9')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const row = page.getByRole('row').filter({ hasText: 'wan1' })
  await expect(row).toContainText('from DHCP')
  await expect(row).toContainText('9.9.9.9')
  // Nothing probes gateways in this test run, so the state stays unknown.
  await expect(row).toContainText('not probed')
  await page.screenshot({ path: shot('61-gateways'), fullPage: true })

  await applyAndConfirm(page)

  await page.reload()
  await expect(page.getByRole('row').filter({ hasText: 'wan1' })).toContainText('Fibre')
})
