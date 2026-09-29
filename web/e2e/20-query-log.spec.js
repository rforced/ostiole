import { expect, test } from './fixtures.js'
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

// The answers need root to collect, so the router's are stood in for: the
// search box and the selects go to it, and older answers are read on.
test('the queries are searched and read on at the router', async ({ page }) => {
  const asked = []
  await page.route('**/api/v1/dns/queries?*', async (route) => {
    const url = new URL(route.request().url())
    asked.push(url.searchParams)
    const before = Number(url.searchParams.get('before') ?? 0)
    const all = Array.from({ length: 300 }, (_, i) => ({
      seq: 300 - i,
      time: '2026-09-26T12:00:00Z',
      client: '10.0.0.2',
      device: 'laptop',
      name: `name${300 - i}.example`,
      type: 'A',
      status: 'ok',
    })).filter((e) => !before || e.seq < before)
    const entries = all.slice(0, 200)
    await route.fulfill({
      json: {
        enabled: true,
        entries,
        more: all.length > 200,
        next: entries.at(-1)?.seq,
        held: 300,
      },
    })
  })
  await page.route('**/api/v1/dns/queries/summary', (route) =>
    route.fulfill({ json: { enabled: true, total: 300, blocked: 0, clients: 1 } }),
  )
  await page.route('**/api/v1/dns/queries/stream', (route) => route.fulfill({ status: 204 }))

  await login(page)
  await page.goto('/services/dns#queries')
  const card = page.getByRole('region', { name: 'Queries' })
  const rows = card.locator('tbody tr')
  await expect(rows).toHaveCount(200)
  // One search box stands where the Name and Client fields were.
  await expect(card.getByLabel('Name', { exact: true })).toHaveCount(0)
  await expect(card.getByLabel('Client', { exact: true })).toHaveCount(0)

  // Scrolling to the foot reads the next page. A click would scroll it
  // into view first, and then find the button gone once that page is in.
  await card.getByRole('button', { name: 'Load more' }).scrollIntoViewIfNeeded()
  await expect(rows).toHaveCount(300)
  await expect(card).toContainText('Start of the log.')

  await card.getByLabel('Status').selectOption('blocked')
  await expect.poll(() => asked.at(-1)?.get('status')).toBe('blocked')
  await card.getByRole('searchbox').fill('laptop')
  await expect.poll(() => asked.at(-1)?.get('q')).toBe('laptop')
})
