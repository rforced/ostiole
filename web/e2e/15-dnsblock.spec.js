import { createServer } from 'node:http'

import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// A local stand-in for a published blocklist, so the test never reaches out
// to the internet. It is written as a hosts file, the commonest shape, with
// the loopback entries a real one carries.
let lists
let listURL
let served = 0
/** Held open on request, so the in-flight state can be looked at. */
let delayMs = 0

test.beforeAll(async () => {
  lists = createServer(async (req, res) => {
    served++
    if (delayMs) await new Promise((r) => setTimeout(r, delayMs))
    res.writeHead(200, { 'Content-Type': 'text/plain' })
    res.end(
      '# Title: a list\n' +
        '127.0.0.1 localhost\n' +
        '0.0.0.0 ads.example.com\n' +
        '0.0.0.0 www.ads.example.com\n' +
        '0.0.0.0 tracker.example.net\n',
    )
  })
  await new Promise((resolve) => lists.listen(0, '127.0.0.1', resolve))
  listURL = `http://127.0.0.1:${lists.address().port}/hosts.txt`
})

test.afterAll(() => lists?.close())

test('subscribe to a blocklist, fetch it, and ask why a name is blocked', async ({ page }) => {
  await login(page)
  await page.goto('/services/dns')
  await expect(page.getByRole('heading', { name: 'DNS' })).toBeVisible()

  await page.getByRole('tab', { name: 'Block lists' }).click()
  await page.getByLabel('DNS blocking enabled').check()

  await page.getByRole('button', { name: 'Add list' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('house')
  await dialog.getByLabel('Description').fill('A list from somewhere')
  await dialog.getByLabel('Fetch from').fill(listURL)
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const row = page.getByRole('row').filter({ hasText: 'house' })
  // The box only knows about lists that have been applied, so it says that
  // rather than "not fetched yet", which would invite a pointless refresh.
  await expect(row).toContainText('not applied yet')
  await page.screenshot({ path: shot('50-dnsblock-lists'), fullPage: true })

  await applyAndConfirm(page)

  // Fetch it. The list is only reached now, not while it was a draft.
  const before = served
  await page.getByRole('button', { name: 'Refresh lists' }).click()
  await expect(row).toContainText('2', { timeout: 15_000 })
  expect(served).toBeGreaterThan(before)

  // Two names, not three: www.ads.example.com is covered by its parent.
  await expect(row).not.toContainText('not applied yet')
  await expect(row).toContainText('hosts')
})

test('the lookup says which list blocks a name, and an allow entry beats it', async ({ page }) => {
  await login(page)
  await page.goto('/services/dns#exceptions')

  const query = page.getByLabel('Name', { exact: true })
  await query.fill('deep.ads.example.com')
  await page.getByRole('button', { name: 'Look up' }).click()

  const result = page.getByText('deep.ads.example.com', { exact: false }).last()
  await expect(result).toBeVisible()
  await expect(page.getByText('Blocked: a list has ads.example.com, which covers it')).toBeVisible()
  await expect(page.getByText('house', { exact: false }).last()).toBeVisible()
  await page.screenshot({ path: shot('51-dnsblock-lookup'), fullPage: true })

  // Allowing the parent lets the child back through.
  await page.getByLabel('Never block').fill('ads.example.com')
  await applyAndConfirm(page)

  await query.fill('deep.ads.example.com')
  await page.getByRole('button', { name: 'Look up' }).click()
  await expect(page.getByText('Not blocked: the allow list has ads.example.com')).toBeVisible()
})

test('a name this box answers for is never blocked', async ({ page }) => {
  await login(page)
  await page.goto('/services/dns#exceptions')

  // 05-services set the local domain to "lan" and added the host printer.
  await page.getByLabel('Never block').fill('ads.example.com')
  await page.getByLabel('Always block').fill('printer.lan')
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  await expect(page.getByRole('alert')).toContainText('take the UI away')
  await page.getByRole('button', { name: 'Discard' }).click()
})

test('enforcement renders firewall rules that keep clients on this resolver', async ({ page }) => {
  await login(page)
  await page.goto('/services/dns#enforcement')

  await page.getByLabel(/Send all plain DNS to this box/).check()
  await page.getByLabel(/Drop DNS over TLS/).check()
  await page.getByLabel(/Ask Firefox not to turn on DNS over HTTPS/).check()
  await page.screenshot({ path: shot('52-dnsblock-enforcement'), fullPage: true })

  await applyAndConfirm(page)

  await page.goto('/system/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.locator('pre')
  await expect(ruleset).toContainText('chain block_dns')
  await expect(ruleset).toContainText('th dport 853 counter drop comment "block:dot"')
  await expect(ruleset).toContainText('redirect to :53 comment "block:dns-redirect"')
})

test('a refresh shows it is working and says what it did', async ({ page }) => {
  await login(page)
  await page.goto('/services/dns#lists')

  const refresh = page.getByRole('button', { name: 'Refresh lists' })
  await expect(refresh).toBeEnabled()

  // Hold the list server open so the in-flight state is not a race.
  delayMs = 2500
  await refresh.click()

  const working = page.getByRole('button', { name: 'Refreshing…' })
  await expect(working).toBeVisible()
  await expect(working).toBeDisabled()
  await expect(page.getByText('Fetching every list')).toBeVisible()
  // The per-row control is out of action too, so it cannot be mashed.
  await expect(
    page
      .getByRole('row')
      .filter({ hasText: 'house' })
      .getByRole('button', { name: 'Refresh', exact: true }),
  ).toBeDisabled()

  delayMs = 0
  await expect(refresh).toBeEnabled({ timeout: 20_000 })
  // The list has not moved since the last test, and it says so rather than
  // leaving the button looking like it did nothing.
  await expect(page.getByText(/Refreshed: .*unchanged/)).toBeVisible()
})

test('a refresh with blocking switched off says why it fetched nothing', async ({ page }) => {
  await login(page)
  await page.goto('/services/dns#lists')
  await page.getByLabel('DNS blocking enabled').uncheck()
  await applyAndConfirm(page)

  await page.getByRole('button', { name: 'Refresh lists' }).click()
  await expect(page.getByText('DNS blocking is off, so no list was fetched')).toBeVisible()

  // Put it back for anything that runs after this.
  await page.getByLabel('DNS blocking enabled').check()
  await applyAndConfirm(page)
})
