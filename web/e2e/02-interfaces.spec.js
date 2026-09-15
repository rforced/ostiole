import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('configure an unmanaged interface and apply it', async ({ page }) => {
  await login(page)
  await page.goto('/interfaces')
  await expect(page.getByRole('heading', { name: 'Interfaces' })).toBeVisible()
  await page.screenshot({ path: shot('10-interfaces'), fullPage: true })

  // The wizard configured the first link as LAN; take the first row still offering "Configure".
  const candidate = page
    .getByRole('row')
    .filter({ has: page.getByRole('button', { name: 'Configure' }) })
    .first()
  const name = (await candidate.locator('td').first().locator('div').first().textContent()).trim()
  // Pin the row by name so it stays valid after it gains an Edit button.
  const row = page.getByRole('row').filter({ has: page.getByText(name, { exact: true }) })
  await row.getByRole('button', { name: 'Configure' }).click()

  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: `Interface ${name}` })).toBeVisible()
  await dialog.getByLabel('Description').fill('Servers')
  await dialog.getByLabel('Zone').selectOption('wan')
  await dialog.getByLabel('Mode').first().selectOption('static')
  await dialog.getByLabel('Address (CIDR)').first().fill('10.77.0.1/24')
  await page.screenshot({ path: shot('11-interface-dialog'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(dialog).toHaveCount(0)

  await expect(page.getByText('Unapplied changes.')).toBeVisible()
  await expect(row.getByText('10.77.0.1/24')).toBeVisible()
  await page.screenshot({ path: shot('12-interfaces-dirty'), fullPage: true })

  await applyAndConfirm(page)

  await page.reload()
  const again = page.getByRole('row').filter({ hasText: name })
  await expect(again.getByText('10.77.0.1/24')).toBeVisible()
  await expect(again.getByText('wan', { exact: true })).toBeVisible()
})

test('validation errors from the server are shown with paths', async ({ page }) => {
  await login(page)
  await page.goto('/interfaces')
  const row = page.getByRole('row').filter({ hasText: '10.77.0.1/24' })
  await row.getByRole('button', { name: 'Edit' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Address (CIDR)').first().fill('not an address')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  const alert = page.getByRole('alert')
  await expect(alert).toContainText('invalid configuration')
  await expect(alert).toContainText('ipv4.address')
  await page.screenshot({ path: shot('13-validation-error'), fullPage: true })
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})

test('add and delete a zone', async ({ page }) => {
  await login(page)
  await page.goto('/interfaces')
  await page.getByRole('tab', { name: 'Zones' }).click()
  await page.getByRole('button', { name: 'Add zone' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('dmz')
  await dialog.getByLabel('Description').fill('Servers')
  await dialog.getByLabel(/Log drops/).check()
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const row = page.getByRole('row').filter({ hasText: 'dmz' })
  await expect(row).toContainText('log drops')
  await page.screenshot({ path: shot('14-zones'), fullPage: true })

  // lan is referenced by an interface and a rule: not deletable.
  await expect(page.getByRole('row').filter({ hasText: /^lan/ }).getByText('in use')).toBeVisible()

  await row.getByRole('button', { name: 'Delete' }).click()
  await row.getByRole('button', { name: 'Delete zone?' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'dmz' })).toHaveCount(0)
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})
