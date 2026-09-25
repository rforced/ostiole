import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('add a device, apply, and wake it', async ({ page }) => {
  await login(page)
  await sidebar(page, 'Services', 'Wake on LAN')
  await expect(
    page.getByRole('heading', { name: 'Wake on LAN', exact: true, level: 1 }),
  ).toBeVisible()
  await expect(page.getByText('No devices.')).toBeVisible()

  await page.getByRole('button', { name: 'Add device' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('NAS')
  // As Windows writes it; the page stores it the way the server reads it.
  await dialog.getByLabel('MAC address').fill('AA-BB-CC-00-00-01')
  // The wizard's LAN is the first inside interface, and the one picked.
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const row = page.getByRole('row').filter({ hasText: 'NAS' })
  await expect(row).toContainText('aa:bb:cc:00:00:01')
  await applyAndConfirm(page)

  // The e2e server is not root, so the packet cannot go out, and the page
  // says why rather than claiming it was sent.
  await row.getByRole('button', { name: 'Wake' }).click()
  await expect(page.getByRole('alert')).toContainText(
    'NAS: sending a wake needs the daemon to run as root',
  )
  await page.screenshot({ path: shot('120-wol'), fullPage: true })
})

test('a cron job wakes a listed device', async ({ page }) => {
  await login(page)
  await sidebar(page, 'System', 'Cron jobs')
  await page.getByRole('button', { name: 'Add cron job' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Morning NAS')
  await dialog.getByLabel('What it does').selectOption('wake')
  await expect(dialog.getByLabel('Device')).toHaveValue(/^wol-/)
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'Morning NAS' })).toContainText('Wake NAS')
  await applyAndConfirm(page)
})
