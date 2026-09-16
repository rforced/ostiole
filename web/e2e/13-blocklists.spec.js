import { createServer } from 'node:http'

import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// A local stand-in for a published blocklist, so the test never reaches
// out to the internet.
let lists
let listURL
let served = 0

test.beforeAll(async () => {
  lists = createServer((req, res) => {
    served++
    res.writeHead(200, { 'Content-Type': 'text/plain' })
    res.end('# a list\n192.0.2.0/24 ; note\n198.51.100.7\n2001:db8:dead::/48\n')
  })
  await new Promise((resolve) => lists.listen(0, '127.0.0.1', resolve))
  listURL = `http://127.0.0.1:${lists.address().port}/drop.txt`
})

test.afterAll(() => lists?.close())

test('a blocklist alias fetches, lands in the ruleset, and refreshes', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await page.getByRole('tab', { name: 'Aliases' }).click()

  await page.getByRole('button', { name: 'Add alias' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('blocklist')
  await dialog.getByLabel('Description').fill('A list from somewhere')
  await dialog.getByLabel('Fetch from').fill(listURL)
  await dialog.getByLabel('Refresh every (hours)').fill('12')
  await dialog.getByLabel('Entries').fill('203.0.113.9')
  await page.screenshot({ path: shot('98-blocklist'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const row = page.getByRole('row').filter({ hasText: 'blocklist' })
  await expect(row).toContainText('not fetched yet')

  // A rule that drops what the list names.
  await page.getByRole('tab', { name: 'Rules' }).click()
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'wan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  const rule = page.getByRole('dialog')
  await rule.getByLabel('Description').fill('Drop the listed networks')
  await rule.getByLabel('Action').selectOption('drop')
  await rule.getByLabel('Match', { exact: true }).first().selectOption('alias')
  await rule.getByLabel('Alias', { exact: true }).selectOption('blocklist')
  await rule.getByRole('button', { name: 'Save to draft' }).click()

  await applyAndConfirm(page)

  // Both families are in the ruleset from the start, because tomorrow's
  // list may have addresses today's does not.
  await page.goto('/system')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.locator('pre')
  await expect(ruleset).toContainText('set alias_blocklist_v4')
  await expect(ruleset).toContainText('set alias_blocklist_v6')
  await expect(ruleset).toContainText('@alias_blocklist_v6')

  // Fetch it, and the cached entries show up against the alias.
  await page.goto('/firewall')
  await page.getByRole('tab', { name: 'Aliases' }).click()
  const listed = page.getByRole('row').filter({ hasText: 'blocklist' })
  await listed.getByRole('button', { name: 'Refresh' }).click()
  // Three from the list; the entry typed in by hand is not "fetched".
  await expect(listed).toContainText('3 fetched')
  expect(served).toBeGreaterThan(0)
  await page.screenshot({ path: shot('99-blocklist-fetched'), fullPage: true })
})

test('a country alias asks for codes, not addresses', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await page.getByRole('tab', { name: 'Aliases' }).click()

  await page.getByRole('button', { name: 'Add alias' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('countries')
  await dialog.getByLabel('Type').selectOption('geoip')
  // There is no URL to give: the source is a system setting.
  await expect(dialog.getByLabel('Fetch from')).toHaveCount(0)
  await expect(dialog).toContainText('two-letter country codes')
  await dialog.getByLabel('Entries').fill('not-a-country')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  // The server catches it, because the browser is not the last word.
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  await expect(page.getByRole('alert')).toContainText('two-letter country code')
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})
