import { expect, test } from './fixtures.js'
import { applyAndConfirm, confirmDialog, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

/** A PEM block around a made-up body, which is all the import and the model check. */
const pem = (type, body) =>
  `-----BEGIN ${type}-----\n${Buffer.from(body).toString('base64')}\n-----END ${type}-----`

/** A client file as a server hands it out, with everything inline. */
const FILE = [
  'client',
  'dev tun',
  'remote vpn.example 1194 udp',
  'remote-cert-tls server',
  '<ca>',
  pem('CERTIFICATE', 'lab CA certificate'),
  '</ca>',
  '<cert>',
  pem('CERTIFICATE', 'lab client certificate'),
  '</cert>',
  '<key>',
  pem('PRIVATE KEY', 'lab client key'),
  '</key>',
  '<tls-crypt>',
  pem('OpenVPN Static key V1', 'lab tls-crypt key'),
  '</tls-crypt>',
  '',
].join('\n')

/** A tunnel's row on the Tunnels tab. */
const tunnelRow = (page, name) =>
  page
    .getByRole('region', { name: 'Tunnels' })
    .getByRole('row')
    .filter({ has: page.getByText(name, { exact: true }) })

test('add a tunnel from a file and apply it', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/openvpn')
  await expect(page.getByRole('heading', { name: 'OpenVPN', level: 1 })).toBeVisible()

  // The e2e server is not root and has no unit for OpenVPN, so the note
  // stands rather than the page claiming a tunnel can run.
  await expect(page.getByRole('note')).toContainText('Not on this router')
  await expect(
    page.getByText("No tunnels. A provider's or a server's client file adds one."),
  ).toBeVisible()

  await page.getByRole('button', { name: 'Add from file' }).click()
  const dialog = page.getByRole('dialog', { name: 'Add OpenVPN tunnel from file' })
  await dialog.getByLabel('File', { exact: true }).fill(FILE)
  await expect(dialog).toContainText('vpn.example:1194')
  await expect(dialog.getByLabel('Interface name')).toHaveValue('ovpn0')
  await expect(dialog.getByLabel('New zone name')).toHaveValue('ovpn0')
  // Nothing in the file asks for the router's way out.
  await expect(dialog.getByLabel('Use as a way out')).not.toBeChecked()
  await expect(dialog.getByLabel('Monitor address')).toHaveCount(0)
  await expect(dialog.getByLabel('Username')).toHaveCount(0)
  await page.screenshot({ path: shot('135-openvpn'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(dialog).toHaveCount(0)

  const tunnel = tunnelRow(page, 'ovpn0')
  await expect(tunnel.locator('[data-label="Zone"]')).toHaveText('ovpn0')
  await expect(tunnel.locator('[data-label="Remote"]')).toHaveText('vpn.example:1194')

  // Without root the server applies no services, so the apply goes through
  // and the tunnel is simply not running.
  await applyAndConfirm(page)
  await page.goto('/vpn/openvpn')
  await expect(tunnel.locator('.badge')).toHaveText('down')
  await expect(page.getByRole('note')).toContainText('Not on this router')

  // The interface is listed with its zone and linked back to its page.
  await sidebar(page, 'Interfaces')
  const row = page.getByRole('row').filter({ hasText: 'ovpn0' })
  await expect(row).toContainText('OpenVPN')
  await row.getByRole('link', { name: 'OpenVPN' }).click()
  await expect(page).toHaveURL(/\/vpn\/openvpn/)
})

test('deleting the tunnel takes its interface with it', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/openvpn')
  await tunnelRow(page, 'ovpn0').getByRole('button', { name: 'Delete' }).click()
  await confirmDialog(page, { typed: 'ovpn0', confirm: 'Delete' })
  await expect(tunnelRow(page, 'ovpn0')).toHaveCount(0)
  await expect(
    page.getByText("No tunnels. A provider's or a server's client file adds one."),
  ).toBeVisible()
  await applyAndConfirm(page)

  await sidebar(page, 'Interfaces')
  await expect(page.getByRole('row').filter({ hasText: 'ovpn0' })).toHaveCount(0)
})
