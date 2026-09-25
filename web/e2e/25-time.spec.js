import { expect, test } from '@playwright/test'

import { applyAndConfirm, confirmDialog, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

const DEFAULTS = ['2.pool.ntp.org']

test('the defaults are listed, and the page says the service is not set up', async ({ page }) => {
  await login(page)
  await sidebar(page, 'Services', 'NTP')
  await expect(page.getByRole('heading', { name: 'NTP', exact: true, level: 1 })).toBeVisible()

  // The e2e server is not root, so there is no unit of ours: the
  // distribution keeps the clock, and the page says so rather than
  // showing a service that is not there.
  await expect(page.getByRole('note')).toContainText('The time service is not set up')
  await expect(page.getByRole('heading', { name: 'Clock' })).toHaveCount(0)

  await expect(
    page.getByText("These follow Ostiole's defaults until you edit the list."),
  ).toBeVisible()
  for (const host of DEFAULTS) {
    await expect(page.getByRole('row').filter({ hasText: host })).toContainText('Unsigned')
  }
  // The wizard switched serving on with the LAN services.
  await expect(page.getByLabel('Answer time requests')).toBeChecked()
})

test('add a server beside the defaults, narrow serving, and apply', async ({ page }) => {
  await login(page)
  await sidebar(page, 'Services', 'NTP')

  await page.getByRole('button', { name: 'Add server' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Server', { exact: true }).fill('192.168.50.5')
  // A new server starts signed, and an address cannot be.
  await expect(dialog.getByRole('alert')).toContainText('Signed answers need a name')
  await expect(dialog.getByRole('button', { name: 'Save to draft' })).toBeDisabled()
  await dialog.getByLabel('Signed answers (NTS)').uncheck()
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  // The defaults became the router's own list, with the new one beside them.
  await expect(page.getByRole('row').filter({ hasText: '192.168.50.5' })).toContainText('Unsigned')
  for (const host of DEFAULTS) {
    await expect(page.getByRole('row').filter({ hasText: host })).toHaveCount(1)
  }
  await expect(page.getByText("These follow Ostiole's defaults")).toHaveCount(0)

  // Serving on the LAN interface by name rather than on every inside one.
  await page.getByRole('button', { name: 'Advanced' }).click()
  await page.getByLabel('Every interface outside external zones').uncheck()
  const picked = page.locator('fieldset').filter({ hasText: 'Answer on' }).getByRole('checkbox')
  await expect(picked.nth(1)).toBeChecked()
  await page.screenshot({ path: shot('110-time'), fullPage: true })

  await applyAndConfirm(page)

  // Time requests reach the router whatever the zone's own rules say.
  await sidebar(page, 'System', 'Ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.getByLabel('confirmed ruleset')
  await expect(ruleset).toContainText('udp dport 123')
  await expect(ruleset).toContainText('service:ntp')
})

test('back to the defaults, served everywhere inside', async ({ page }) => {
  await login(page)
  await sidebar(page, 'Services', 'NTP')
  await page
    .getByRole('row')
    .filter({ hasText: '192.168.50.5' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await confirmDialog(page)
  await expect(page.getByRole('row').filter({ hasText: '192.168.50.5' })).toHaveCount(0)

  // What is left is a copy of the defaults, which would stop following
  // them; the page offers the way back.
  await page.getByRole('button', { name: 'Use defaults' }).click()
  await expect(
    page.getByText("These follow Ostiole's defaults until you edit the list."),
  ).toBeVisible()

  await page.getByRole('button', { name: 'Advanced' }).click()
  await page.getByLabel('Every interface outside external zones').check()
  await applyAndConfirm(page)
})
