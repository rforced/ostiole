import { expect, test } from './fixtures.js'
import { confirmDialog, login, sidebar } from './helpers.js'

// An alias nothing names is listed as unused and goes with the header's
// Remove; a rule switched off is listed under Disabled and stays.
test('lists an unused alias and a disabled rule, and removes the alias', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')

  await sidebar(page, 'Aliases')
  await page.getByRole('button', { name: 'Add alias' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('spare_hosts')
  await dialog.getByLabel('Entries').fill('203.0.113.77')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'spare_hosts' })).toBeVisible()

  await sidebar(page, 'Rules')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'lan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Old printer')
  await dialog.getByLabel('Protocol').selectOption('icmp')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const rule = page.getByRole('row').filter({ hasText: 'Old printer' })
  await rule.getByRole('checkbox').uncheck()

  await sidebar(page, 'System', 'Configuration')
  await page.getByRole('tab', { name: 'Unused' }).click()
  const alias = page.getByRole('row').filter({ hasText: 'spare_hosts' })
  await expect(alias).toContainText('Alias')
  await expect(alias).toContainText('Nothing names it')
  const off = page.getByRole('row').filter({ hasText: 'Old printer' })
  await expect(off).toContainText('Rule')
  await expect(off).toContainText('Switched off')
  const rows = await page.getByRole('row').allTextContents()
  const at = (needle) => rows.findIndex((t) => t.includes(needle))
  expect(at('spare_hosts')).toBeGreaterThan(at('Unused'))
  expect(at('Disabled')).toBeGreaterThan(at('spare_hosts'))
  expect(at('Old printer')).toBeGreaterThan(at('Disabled'))

  await page.getByRole('button', { name: 'Remove 1 unused item' }).click()
  await expect(page.getByRole('dialog')).toContainText('alias spare_hosts')
  await confirmDialog(page, { confirm: 'Remove' })
  await expect(alias).toHaveCount(0)
  await expect(page.getByText('No unused items.')).toBeVisible()
  await expect(off).toBeVisible()
  await expect(page.getByText('Removed 1 unused item.')).toBeVisible()

  // The alias came and went in the draft; the switched-off rule is the change.
  await expect(page.getByText('Unapplied changes.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Show 1 change' })).toBeVisible()
})
