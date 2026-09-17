import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('add an alias, a rule using it, and a port forward, then apply', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await expect(page).toHaveURL(/\/firewall\/rules$/)
  await expect(page.getByRole('heading', { name: 'Rules' })).toBeVisible()

  // Alias
  await sidebar(page, 'Aliases')
  await page.getByRole('button', { name: 'Add alias' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('admins')
  await dialog.getByLabel('Entries').fill('203.0.113.10\n2001:db8::10')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'admins' })).toContainText('203.0.113.10')

  // Rule on wan: allow tcp 443 from the alias to this firewall
  await sidebar(page, 'Rules')
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

  // The rules Ostiole adds on its own sit around these, in the order the
  // kernel meets them, and link to their setting instead of being editable.
  const everything = page.getByRole('row').filter({ hasText: 'Everything else' })
  await expect(everything).toBeVisible()
  await expect(everything.getByRole('checkbox')).toHaveCount(0)
  const order = await page.getByRole('row').allTextContents()
  const at = (needle) => order.findIndex((t) => t.includes(needle))
  expect(at('Replies and related traffic')).toBeLessThan(at('Ping'))
  expect(at('Everything else')).toBeGreaterThan(at('Admin HTTPS'))

  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'lan' }).click()
  const lockout = page.getByRole('row').filter({ hasText: 'Anti-lockout' })
  await expect(lockout).toContainText('this firewall : ')
  await expect(lockout.getByRole('link', { name: 'Change' })).toBeVisible()
  await expect(
    page.getByRole('row').filter({ hasText: 'DNS queries to this firewall' }),
  ).toBeVisible()

  // Port forward
  await sidebar(page, 'NAT')
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

  // The page has its own URL, so a reload comes back to NAT, not to Rules.
  await page.reload()
  await expect(page).toHaveURL(/\/firewall\/nat$/)
  await expect(page.getByRole('row').filter({ hasText: 'Web server' })).toBeVisible()

  await sidebar(page, 'Rules')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'wan' }).click()
  const after = page.getByRole('row').filter({ hasText: /Admin HTTPS|Ping/ })
  await expect(after.nth(0)).toContainText('Ping')
  await expect(after.nth(1)).toContainText('Admin HTTPS')
})

test('an alias in use cannot be deleted; a rule can be disabled', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await sidebar(page, 'Aliases')
  await expect(
    page.getByRole('row').filter({ hasText: 'admins' }).getByText('in use'),
  ).toBeVisible()

  await sidebar(page, 'Rules')
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

test('schedule a rule, reflect a port forward, and map an address 1:1', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')

  await sidebar(page, 'Schedules')
  await page.getByRole('button', { name: 'Add schedule' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('workday')
  await dialog.getByLabel('Description').fill('Office hours')
  await dialog.getByLabel('From').fill('08:30')
  await dialog.getByLabel('To').fill('17:30')
  for (const day of ['monday', 'tuesday']) {
    await dialog.getByRole('checkbox', { name: day }).check()
  }
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const scheduleRow = page.getByRole('row').filter({ hasText: 'workday' })
  await expect(scheduleRow).toContainText('mon, tue')

  // A rule that only matches inside the window.
  await sidebar(page, 'Rules')
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Streaming during work')
  await dialog.getByLabel('Action').selectOption('drop')
  await dialog.getByLabel('Schedule').selectOption('workday')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'Streaming during work' })).toContainText(
    'workday',
  )

  // NAT reflection and a 1:1 mapping.
  await sidebar(page, 'NAT')
  await page
    .getByRole('row')
    .filter({ hasText: 'Web server' })
    .getByRole('button', { name: 'Edit' })
    .click()
  dialog = page.getByRole('dialog')
  await dialog.getByRole('checkbox', { name: /NAT reflection/ }).check()
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'Web server' })).toContainText('reflection')

  await page.getByRole('button', { name: 'Add 1:1 NAT' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Mail server')
  await dialog.getByLabel('External address').fill('203.0.113.10')
  await dialog.getByLabel('Internal address').fill('10.0.0.25')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'Mail server' })).toContainText('10.0.0.25')
  await page.screenshot({ path: shot('34-nat-one-to-one'), fullPage: true })

  await applyAndConfirm(page)

  // The rendered ruleset carries all three.
  await page.goto('/system/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.locator('pre')
  await expect(ruleset).toContainText('meta day { "Monday", "Tuesday" } meta hour "08:30"-"17:30"')
  await expect(ruleset).toContainText('reflect:')
  await expect(ruleset).toContainText('snat ip to 203.0.113.10')
})
