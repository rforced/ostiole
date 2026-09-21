import { expect, test } from '@playwright/test'

import { login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// The whole flow stays in one test because a draft does not survive a full
// page load; the sidebar is used to move around, and the draft is
// discarded at the end.
test('ask the ISP for a prefix and hand a subnet to the LAN', async ({ page }) => {
  await login(page)
  await page.goto('/interfaces')

  // Pick rows by their zone cell, which holds the zone name and nothing else.
  const inZone = (zone) =>
    page
      .getByRole('row')
      .filter({ has: page.getByText(zone, { exact: true }) })
      .first()
  const wanRow = inZone('wan')
  const lanRow = inZone('lan')
  const wan = (await wanRow.locator('td').first().locator('div').first().textContent()).trim()

  // Until something asks upstream for a prefix, nothing can be delegated.
  await lanRow.getByRole('button', { name: 'Edit' }).click()
  let dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('option', { name: /Delegated/ })).toBeDisabled()
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  // The WAN asks for a /56.
  await wanRow.getByRole('button', { name: 'Edit' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Mode').last().selectOption('dhcp')
  await dialog.getByLabel('Ask for a prefix').fill('::/56')
  await page.screenshot({ path: shot('95-prefix-hint'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(wanRow).toContainText(`DHCPv6 + ::/56`)

  // Now the LAN can take a subnet of it.
  await lanRow.getByRole('button', { name: 'Edit' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Mode').last().selectOption('delegated')
  await dialog.getByLabel('Prefix from').selectOption(wan)
  await dialog.getByLabel('Subnet').fill('1')
  await page.screenshot({ path: shot('96-delegated'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(lanRow).toContainText(`subnet 1 of ${wan}`)

  // A second interface cannot take the same subnet: the server says so.
  await lanRow.getByRole('button', { name: 'Edit' }).click()
  dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Subnet')).toHaveValue('1')
  await dialog.getByLabel('Subnet').fill('9999')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  await expect(page.getByRole('alert')).toContainText('subnetId')

  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})
