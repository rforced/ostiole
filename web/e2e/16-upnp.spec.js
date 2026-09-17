import { expect, test } from '@playwright/test'

import { applyAndConfirm, confirmDialog, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('switch on port mapping, write an access list, and apply', async ({ page }) => {
  await login(page)
  await page.goto('/services/upnp')
  await expect(page.getByRole('heading', { name: 'Port mapping', exact: true })).toBeVisible()

  // Off is the default, and nothing about it is filled in.
  const enabled = page.getByLabel('Port mapping enabled')
  await expect(enabled).not.toBeChecked()
  await enabled.check()

  // Enabled on its own answers nothing, and the page says so until a
  // protocol is chosen.
  await expect(page.getByText('With neither switched on, nothing answers.')).toBeVisible()
  await page.getByLabel('UPnP IGD').check()
  await page.getByLabel('PCP and NAT-PMP').check()
  await expect(page.getByText('With neither switched on, nothing answers.')).toHaveCount(0)

  // Only interfaces in an external zone are offered: a port opened
  // anywhere else reaches nothing.
  const external = page.locator('#upnp-ext')
  const wan = await external.locator('option').nth(1).getAttribute('value')
  expect(wan).toBeTruthy()
  await external.selectOption(wan)
  // The external interface is never somewhere clients ask from.
  await page.getByLabel('Every interface outside external zones').uncheck()
  await expect(page.getByRole('checkbox', { name: wan, exact: true })).toHaveCount(0)
  await page.getByLabel('Every interface outside external zones').check()

  await page.getByLabel('Refuse anything no entry allows').check()
  await expect(page.getByText('No entries. Every request is refused.')).toBeVisible()

  await page.getByRole('button', { name: 'Add entry' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Client').fill('192.168.50.0/24')
  await dialog.getByLabel('Description').fill('The LAN may map high ports')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const allow = page.getByRole('row').filter({ hasText: '192.168.50.0/24' })
  await expect(allow).toContainText('1024-65535')
  await expect(allow).toContainText('allow')

  // The list is read top to bottom, so a deny written after the allow has
  // to be movable above it.
  await page.getByRole('button', { name: 'Add entry' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Action').selectOption('deny')
  await dialog.getByLabel('Client').fill('192.168.50.77')
  await dialog.getByLabel('External ports').fill('1-65535')
  await dialog.getByLabel('Internal ports').fill('1-65535')
  await dialog.getByLabel('Description').fill('Except this one host')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const rows = page.getByRole('row')
  await expect(rows.nth(1)).toContainText('192.168.50.0/24')
  await page.getByRole('button', { name: 'Move entry 2 up' }).click()
  await expect(rows.nth(1)).toContainText('192.168.50.77')
  await expect(rows.nth(2)).toContainText('192.168.50.0/24')
  await page.screenshot({ path: shot('100-upnp-service'), fullPage: true })

  await applyAndConfirm(page)

  // The chains the daemon is pointed at are in Ostiole's own table, and
  // clients can reach the ports it answers on.
  await sidebar(page, 'System', 'Ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.getByLabel('confirmed ruleset')
  await expect(ruleset).toContainText('chain upnp_prerouting')
  await expect(ruleset).toContainText('jump upnp_forward')
  await expect(ruleset).toContainText('service:upnp')
})

test('the page is honest about the daemon not being installed', async ({ page }) => {
  await login(page)
  await page.goto('/services/upnp')
  // The e2e server is not root and has no miniupnpd, so the warning stands
  // rather than the service claiming to run.
  await expect(page.getByRole('note')).toContainText(
    'Port mapping is not set up on this router yet',
  )

  // Mappings are read out of the ruleset, not from a lease file: the stub
  // answers with one the daemon would have written.
  await page.getByRole('tab', { name: 'Mappings' }).click()
  const mapping = page.getByRole('row').filter({ hasText: '192.168.50.40' })
  await expect(mapping).toContainText('UDP')
  await expect(mapping).toContainText('19132')
  await page.screenshot({ path: shot('101-upnp-mappings'), fullPage: true })
})

test('deleting the last access list entry leaves the default deny in charge', async ({ page }) => {
  await login(page)
  await page.goto('/services/upnp')
  for (const client of ['192.168.50.77', '192.168.50.0/24']) {
    await page
      .getByRole('row')
      .filter({ hasText: client })
      .getByRole('button', { name: 'Delete' })
      .click()
    await confirmDialog(page)
  }
  await expect(page.getByText('No entries. Every request is refused.')).toBeVisible()
  await applyAndConfirm(page)
})
