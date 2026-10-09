import { readFileSync } from 'node:fs'

import { expect, test } from './fixtures.js'
import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

/** A tunnel's row on the Tunnels tab. */
const tunnelRow = (page, name) =>
  page
    .getByRole('region', { name: 'Tunnels' })
    .getByRole('row')
    .filter({ has: page.getByText(name, { exact: true }) })

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
  await dialog.getByLabel('IPv4 address').fill('10.66.0.1/24')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const tunnel = tunnelRow(page, 'wg0')
  await expect(tunnel).toContainText('Road warriors')
  await expect(tunnel).toContainText(tunnelKey)
  await expect(tunnel).toContainText('udp/51820')

  await page.getByRole('tab', { name: 'Peers' }).click()
  await page.getByRole('button', { name: 'Add peer' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('laptop')
  await dialog.getByLabel('Keys').selectOption('paste')
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
  // The test server makes no devices, so the kernel has none and says so.
  await page.goto('/vpn/wireguard#tunnels')
  await expect(tunnelRow(page, 'wg0')).toContainText('10.66.0.1/24')
  await expect(tunnelRow(page, 'wg0').locator('.badge-warn')).toHaveText('down')
  await page.goto('/diagnostics/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  await expect(page.locator('pre')).toContainText('service:wireguard')
})

test('make a device on the page and hand it its file', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/wireguard')
  const key = tunnelRow(page, 'wg0').locator('[data-label="Public key"]')
  await expect(key).not.toHaveText('')
  const tunnelKey = (await key.textContent()).trim()
  await page.getByRole('tab', { name: 'Peers' }).click()
  await page.getByRole('button', { name: 'Add peer' }).click()
  const dialog = page.getByRole('dialog')
  // A tunnel that listens makes a device's keys unless told otherwise.
  await expect(dialog.getByLabel('Keys')).toHaveValue('make')
  await dialog.getByLabel('Name').fill('phone')
  await expect(dialog.getByLabel('Allowed addresses')).toHaveValue('10.66.0.3/32')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  await expect(dialog.getByRole('img', { name: 'QR code of the file for phone' })).toBeVisible()
  await dialog.getByLabel('Endpoint').fill('vpn.example.com')
  const download = page.waitForEvent('download')
  await dialog.getByRole('button', { name: 'Download' }).click()
  const file = await download
  expect(file.suggestedFilename()).toBe('wg0-phone.conf')
  const text = readFileSync(await file.path(), 'utf8')
  expect(text).toMatch(
    /^\[Interface\]\nPrivateKey = [A-Za-z0-9+/]{43}=\nAddress = 10\.66\.0\.3\/32\n/,
  )
  expect(text).toContain(`[Peer]\nPublicKey = ${tunnelKey}\nPresharedKey = `)
  expect(text).toContain('Endpoint = vpn.example.com:51820')
  await page.screenshot({ path: shot('60-vpn-device'), fullPage: true })
  await dialog.getByRole('button', { name: 'Close' }).last().click()

  // Only the public key reached the draft.
  const peer = page.getByRole('row').filter({ hasText: 'phone' })
  await expect(peer).toContainText('10.66.0.3/32')
  await expect(peer).toContainText('PSK')
  await applyAndConfirm(page)
})

test('make new keys for a device and hand it the new file', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/wireguard#peers')
  const row = page.getByRole('row').filter({ hasText: 'phone' })
  await row.getByRole('button', { name: 'Edit' }).click()
  // Hidden from the accessibility tree while the confirm is open.
  const peer = page.getByRole('dialog', { name: 'Peer phone', includeHidden: true })
  const oldKey = await peer.getByLabel('Public key').inputValue()
  await peer.getByRole('button', { name: 'Make new keys' }).click()

  // The confirm is asked from inside the peer's dialog, so it has to paint
  // over it. A click reaches it either way, since the dialog underneath
  // takes no pointer events.
  const confirm = page.getByRole('dialog', { name: 'Make new keys for phone?' })
  const z = (d) => d.evaluate((el) => Number(getComputedStyle(el).zIndex))
  expect(await z(confirm)).toBeGreaterThan(await z(peer))
  await confirm.getByLabel('Type phone to confirm').fill('phone')
  await confirm.getByRole('button', { name: 'Make new keys', exact: true }).click()

  const dialog = page.getByRole('dialog', { name: 'File for phone' })
  await expect(dialog.getByRole('img', { name: 'QR code of the file for phone' })).toBeVisible()
  await expect(dialog.locator('pre')).toContainText(/PrivateKey = [A-Za-z0-9+/]{43}=/)
  await expect(dialog.locator('pre')).toContainText('Address = 10.66.0.3/32')
  await dialog.getByRole('button', { name: 'Close' }).last().click()

  await expect(row).toContainText('PSK')
  await row.getByRole('button', { name: 'Edit' }).click()
  const again = page.getByRole('dialog', { name: 'Peer phone' })
  await expect(again.getByLabel('Public key')).not.toHaveValue(oldKey)
  await again.getByRole('button', { name: 'Cancel' }).click()
  await applyAndConfirm(page)
})

test('a bad peer key is rejected by the server', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/wireguard#peers')
  await page.getByRole('button', { name: 'Add peer' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('broken')
  await dialog.getByLabel('Keys').selectOption('paste')
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
