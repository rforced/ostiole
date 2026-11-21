import { expect, test } from './fixtures'
import { applyAndConfirm, confirmDialog, login, shot, sidebar } from './helpers'

test.describe.configure({ mode: 'serial' })

test('join a tailnet, advertise the LAN, and apply', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/tailscale')
  await expect(page.getByRole('heading', { name: 'Tailscale', level: 1 })).toBeVisible()

  // The e2e server is not root and has no tailscaled, so the note stands
  // rather than the page claiming a node.
  await expect(page.getByRole('note')).toContainText('Not on this router')

  await page.getByRole('button', { name: 'Join a tailnet' }).click()
  await expect(page.getByLabel('Port', { exact: true })).toHaveValue('41641')

  // The button writes the zone's networks down masked, so the model takes
  // them without an argument about host bits.
  await page
    .getByRole('button', { name: /Add \w+'s networks/ })
    .first()
    .click()
  await expect(page.getByLabel('Advertise routes')).not.toHaveValue('')
  await page.getByLabel('Advertise as exit node').check()
  await page.screenshot({ path: shot('110-tailscale'), fullPage: true })

  await applyAndConfirm(page)

  // The port peers dial is open, and the rest comes from the zone.
  await sidebar(page, 'Diagnostics', 'Ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  await expect(page.getByLabel('confirmed ruleset')).toContainText('service:tailscale')

  // The interface is a real one, listed with its zone and linked back here.
  await sidebar(page, 'Interfaces')
  const row = page.getByRole('row').filter({ hasText: 'tailscale0' })
  await expect(row).toContainText('tailscale')
  await row.getByRole('link', { name: 'Tailscale' }).click()
  await expect(page).toHaveURL(/\/vpn\/tailscale/)
})

test('peers say nothing while the daemon is not running', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/tailscale')
  await page.getByRole('tab', { name: 'Peers' }).click()
  await expect(page.getByRole('cell', { name: 'Tailscale is not running.' })).toBeVisible()
})

test('route through a Tailscale exit node gateway and apply', async ({ page }) => {
  await login(page)
  // Turn off advertiseExitNode first: Tailscale cannot advertise and use an exit node at once.
  await page.goto('/vpn/tailscale')
  await page.getByLabel('Advertise as exit node').uncheck()

  // In-app navigation keeps the draft; a page load would drop it.
  await sidebar(page, 'Routing')
  await page.getByRole('button', { name: 'Add gateway' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('amsterdam')
  await dialog.getByLabel('Interface').selectOption('tailscale0')
  await dialog.getByLabel('Exit node').fill('ams-1')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const row = page.getByRole('row').filter({ hasText: 'amsterdam' })
  await expect(row).toContainText('ams-1')
  await expect(row).toContainText('the exit node')

  await sidebar(page, 'Firewall', 'Rules')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'lan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Route via Tailscale exit node')
  await dialog.getByLabel('Route through').selectOption('amsterdam')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  await applyAndConfirm(page)

  await sidebar(page, 'Diagnostics', 'Ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.getByLabel('confirmed ruleset')
  await expect(ruleset).toContainText('tunnel-nat:tailscale0')
  await expect(ruleset).toContainText('tunnel-mss')
  await expect(ruleset).toContainText('kill-switch:amsterdam')
})

test('removing the node takes its interface with it', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/tailscale')
  await page.getByRole('button', { name: 'Delete' }).click()
  await confirmDialog(page, { typed: 'tailscale0', confirm: 'Delete' })
  await expect(page.getByRole('button', { name: 'Join a tailnet' })).toBeVisible()
  await applyAndConfirm(page)
})
