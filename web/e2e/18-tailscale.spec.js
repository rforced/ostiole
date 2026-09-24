import { expect, test } from '@playwright/test'

import { applyAndConfirm, confirmDialog, login, shot, sidebar } from './helpers.js'

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
  await sidebar(page, 'System', 'Ruleset')
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

test('removing the node takes its interface with it', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/tailscale')
  await page.getByRole('button', { name: 'Delete' }).click()
  await confirmDialog(page, { typed: 'tailscale0', confirm: 'Delete' })
  await expect(page.getByRole('button', { name: 'Join a tailnet' })).toBeVisible()
  await applyAndConfirm(page)
})
