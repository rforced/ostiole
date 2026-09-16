import { expect, test } from '@playwright/test'

import { login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// Everything here stays in the draft and is discarded at the end, so the
// machine's own interfaces are only rearranged on paper. Navigation is by
// the sidebar rather than page.goto, which would throw the draft away.
test('build a bridge and a bond from the machine s own links', async ({ page }) => {
  await login(page)
  await page.goto('/interfaces')

  // 02-interfaces.spec.js put a spare link in the wan zone at 10.77.0.1/24.
  // Using that one keeps the LAN, and the DHCP scope on it, out of this.
  const addressed = page.getByRole('row').filter({ hasText: '10.77.0.1/24' })
  const spare = (await addressed.locator('td').first().locator('div').first().textContent()).trim()
  // Pin the row by name: the address is about to move to the bridge.
  const spareRow = page.getByRole('row').filter({ has: page.getByText(spare, { exact: true }) })

  await page.getByRole('button', { name: 'Add bridge' }).click()
  let dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Name')).toHaveValue('br0')
  await dialog.getByLabel('Description').fill('Switch ports')
  // The warning is the point: a port gives up its zone and address.
  await expect(dialog.getByText(`in zone wan`).first()).toBeVisible()
  await dialog.getByRole('checkbox', { name: spare, exact: false }).first().check()
  await dialog.getByText('Spanning tree', { exact: false }).click()
  await page.screenshot({ path: shot('90-bridge'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const bridgeRow = page.getByRole('row').filter({ hasText: 'br0' }).first()
  await expect(bridgeRow).toContainText(`bridge of ${spare}`)
  // The member gave up its zone and its configured address to the bridge.
  // Its live address stays until the draft is applied.
  await expect(spareRow).toContainText('unassigned')
  await expect(spareRow).toContainText('no address')

  // A bond cannot take a link the bridge already has.
  await page.getByRole('button', { name: 'Add bond' }).click()
  dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Name')).toHaveValue('bond0')
  await expect(dialog.getByText('already in br0')).toBeVisible()

  // Each mode brings out only the settings it uses.
  await dialog.getByLabel('Mode').selectOption('802.3ad')
  await expect(dialog.getByLabel('LACP rate')).toBeVisible()
  await expect(dialog.getByLabel('Transmit hash policy')).toBeVisible()
  await expect(dialog.getByLabel('Preferred interface')).toHaveCount(0)
  await dialog.getByLabel('Mode').selectOption('active-backup')
  await expect(dialog.getByLabel('LACP rate')).toHaveCount(0)
  await expect(dialog.getByLabel('Preferred interface')).toBeVisible()
  await page.screenshot({ path: shot('91-bond'), fullPage: true })

  // An empty bond is refused rather than written.
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(dialog.getByRole('alert')).toContainText('needs at least one interface')
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  // Editing the bridge reopens it with its members.
  await bridgeRow.getByRole('button', { name: 'Members' }).click()
  dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Name')).toBeDisabled()
  await expect(dialog.getByLabel('Description')).toHaveValue('Switch ports')
  await expect(dialog.getByRole('checkbox', { name: spare, exact: false }).first()).toBeChecked()
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  // The bridge is part of a draft the server accepts: rendering it is how
  // the System page proves that. This test server manages no network
  // units, so it says so rather than showing an empty box.
  await page.getByRole('link', { name: 'System' }).click()
  await page.getByRole('button', { name: 'Render the draft' }).click()
  await expect(page.locator('pre')).toContainText('table inet ostiole')
  await page.getByRole('button', { name: 'Render network units' }).click()
  await expect(page.getByRole('alert')).toContainText('managing no network units')

  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})
