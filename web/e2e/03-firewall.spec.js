import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('add an alias, a rule using it, and a port forward, then apply', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await expect(page.getByRole('heading', { name: 'Firewall' })).toBeVisible()

  // Alias
  await page.getByRole('tab', { name: 'Aliases' }).click()
  await page.getByRole('button', { name: 'Add alias' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('admins')
  await dialog.getByLabel('Entries').fill('203.0.113.10\n2001:db8::10')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'admins' })).toContainText('203.0.113.10')

  // Rule on wan: allow tcp 443 from the alias to this firewall
  await page.getByRole('tab', { name: 'Rules' }).click()
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'wan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Admin HTTPS')
  await dialog.getByLabel('Protocol').selectOption('tcp')
  await dialog.getByLabel('Match', { exact: true }).first().selectOption('alias')
  await dialog.getByLabel('Alias', { exact: true }).selectOption('admins')
  await dialog.getByLabel('Match', { exact: true }).last().selectOption('self')
  await dialog.getByLabel('Ports', { exact: true }).last().selectOption('ports')
  await dialog.getByLabel('Port list').last().fill('443')
  await dialog.getByLabel('Log matches').check()
  await page.screenshot({ path: shot('20-rule-dialog'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const rule = page.getByRole('row').filter({ hasText: 'Admin HTTPS' })
  await expect(rule).toContainText('@admins')
  await expect(rule).toContainText('this firewall : 443')
  await expect(rule).toContainText('log')

  // Second rule, then move it above the first
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Ping')
  await dialog.getByLabel('Protocol').selectOption('icmp')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const rows = page.getByRole('row').filter({ hasText: /Admin HTTPS|Ping/ })
  await expect(rows.nth(1)).toContainText('Ping')
  await page
    .getByRole('button', { name: /Move r-\w+ up/ })
    .last()
    .click()
  await expect(rows.nth(0)).toContainText('Ping')
  await page.screenshot({ path: shot('21-rules'), fullPage: true })

  // Port forward
  await page.getByRole('tab', { name: 'NAT' }).click()
  await page.getByRole('button', { name: 'Add port forward' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Web server')
  await dialog.getByLabel('Ports').fill('80, 443')
  await dialog.getByLabel('Target address').fill('192.168.50.10')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'Web server' })).toContainText(
    '192.168.50.10',
  )
  await page.screenshot({ path: shot('22-nat'), fullPage: true })

  await applyAndConfirm(page)

  await page.reload()
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'wan' }).click()
  const after = page.getByRole('row').filter({ hasText: /Admin HTTPS|Ping/ })
  await expect(after.nth(0)).toContainText('Ping')
  await expect(after.nth(1)).toContainText('Admin HTTPS')
  await page.getByRole('tab', { name: 'NAT' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'Web server' })).toBeVisible()
})

test('an alias in use cannot be deleted; a rule can be disabled', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await page.getByRole('tab', { name: 'Aliases' }).click()
  await expect(
    page.getByRole('row').filter({ hasText: 'admins' }).getByText('in use'),
  ).toBeVisible()

  await page.getByRole('tab', { name: 'Rules' }).click()
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'wan' }).click()
  const rule = page.getByRole('row').filter({ hasText: 'Ping' })
  await rule.getByRole('checkbox').uncheck()
  await expect(page.getByText('Unapplied changes.')).toBeVisible()
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(rule.getByRole('checkbox')).toBeChecked()
})

test('add a static route', async ({ page }) => {
  await login(page)
  await page.goto('/routing')
  await page.getByRole('button', { name: 'Add static route' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Destination network').fill('10.200.0.0/16')
  await dialog.getByLabel('Gateway').fill('192.168.50.254')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: '10.200.0.0/16' })).toContainText(
    '192.168.50.254',
  )
  await page.screenshot({ path: shot('23-routing'), fullPage: true })
  await applyAndConfirm(page)
})
