import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('switch the query log on, and see why it has nothing to show', async ({ page }) => {
  await login(page)
  await page.goto('/services/dns#queries')
  await expect(page.getByRole('heading', { name: 'Query log' })).toBeVisible()

  // Off by default, and the bounds only appear once it is on.
  const keep = page.getByLabel('Query log enabled')
  await expect(keep).not.toBeChecked()
  await expect(page.getByLabel('Entries')).toHaveCount(0)

  await keep.check()
  await page.getByLabel('Entries').fill('5000')
  await page.screenshot({ path: shot('55-dns-query-log'), fullPage: true })

  await applyAndConfirm(page)

  // The setting lives on the DNS server, not on blocking.
  const cfg = await (await page.request.get('/api/v1/config')).json()
  expect(cfg.services.dns.queryLog).toEqual({ enabled: true, entries: 5000 })
  expect(cfg.blocking?.queryLog).toBeUndefined()

  await page.reload()
  await expect(page.getByLabel('Query log enabled')).toBeChecked()
  await expect(page.getByLabel('Entries')).toHaveValue('5000')

  // Collecting answers needs the kernel, and this server is not root, so
  // the log region says so rather than pretending to be empty.
  await expect(page.getByText('query log not available')).toBeVisible()
})

test('switching it off takes the log away and remembers the size', async ({ page }) => {
  await login(page)
  await page.goto('/services/dns#queries')
  await page.getByLabel('Query log enabled').uncheck()
  await expect(page.getByLabel('Entries')).toHaveCount(0)
  await applyAndConfirm(page)

  // Off is the absence of enabled; the size stays so turning it back on
  // does not ask for it again.
  const cfg = await (await page.request.get('/api/v1/config')).json()
  expect(cfg.services.dns.queryLog?.enabled).toBeUndefined()
  expect(cfg.services.dns.queryLog?.entries).toBe(5000)
})

test('the rules page links the log rule to the setting that made it', async ({ page }) => {
  await login(page)
  await page.goto('/services/dns#queries')
  await page.getByLabel('Query log enabled').check()
  await applyAndConfirm(page)

  await page.goto('/firewall/rules')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'lan' }).click()
  const row = page.getByRole('row').filter({ hasText: 'DNS query logger' })
  await expect(row).toBeVisible()
  await expect(row.getByRole('link', { name: 'Edit' })).toHaveAttribute(
    'href',
    '/services/dns#queries',
  )

  // Put it back: the log is off by default and the rest of the suite
  // should see the router it expects.
  await page.goto('/services/dns#queries')
  await page.getByLabel('Query log enabled').uncheck()
  await applyAndConfirm(page)
})
