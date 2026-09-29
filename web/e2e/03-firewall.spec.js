import { expect, test } from './fixtures.js'
import { applyAndConfirm, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('add an alias, a rule using it, and a port forward, then apply', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await expect(page).toHaveURL(/\/firewall\/rules$/)
  await expect(page.getByRole('heading', { name: 'Rules', level: 1 })).toBeVisible()

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
  await expect(lockout.getByRole('link', { name: 'Edit' })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'DNS queries' })).toBeVisible()

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
  const alias = page.getByRole('row').filter({ hasText: 'admins' })
  await expect(alias.locator('td[data-label="Used by"]')).toHaveText(/^rule \S+ source/)
  await expect(alias.getByRole('button', { name: 'Delete' })).toBeDisabled()

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

// Protection is off until somebody turns it on, and then it is in the ruleset
// the router runs, on the zone that faces the internet.
test('flood and scan protection render into the ruleset', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await sidebar(page, 'Protection')
  await expect(page.getByLabel(/^wan/)).toBeChecked()
  await expect(page.getByLabel(/^lan/)).not.toBeChecked()
  const flood = page.getByLabel('Connection flood enabled')
  const scan = page.getByLabel('Port scan enabled')
  await expect(flood).not.toBeChecked()
  await flood.check()
  await expect(page.getByLabel('Connections', { exact: true })).toHaveValue('30')
  await scan.check()
  await page.screenshot({ path: shot('35-protection'), fullPage: true })
  await applyAndConfirm(page)

  await page.goto('/system/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.locator('pre')
  await expect(ruleset).toContainText('set synflood_wan_v4')
  await expect(ruleset).toContainText('set scanners_wan_v4')
  await expect(ruleset).toContainText('protect:scanner')
  await expect(ruleset).not.toContainText('synflood_lan')
  await expect(ruleset).not.toContainText('icmpflood')

  // Off again, for the specs after this one.
  await page.goto('/firewall')
  await sidebar(page, 'Protection')
  await flood.uncheck()
  await scan.uncheck()
  await applyAndConfirm(page)
})

// The log is read from the kernel, which takes root, and this server is not
// root: the page says so rather than showing an empty log.
test('the log says what it needs to read the kernel', async ({ page }) => {
  await login(page)
  await page.goto('/firewall/log')
  const log = page.getByRole('region', { name: 'Logs' })
  await expect(log.getByRole('alert')).toHaveText(
    'The firewall log needs the daemon to run as root.',
  )
  await expect(log.getByRole('button', { name: 'Live' })).toBeVisible()
  await expect(log.getByLabel('Show')).toHaveValue('all')
  await expect(log.getByRole('cell', { name: 'No packets logged.' })).toBeVisible()
})

// How much of the log is kept, and what it costs, is set beside it.
test('the log keeps its own settings', async ({ page }) => {
  await login(page)
  await page.goto('/firewall/log')
  const card = page.getByRole('region', { name: 'Firewall log' })
  await expect(card).toContainText('50,000 is the default.')
  await expect(card).toContainText('All logs:')
  await card.getByLabel('Days').fill('3')
  await card.getByLabel('Log dropped packets').check()
  await applyAndConfirm(page)
  const cfg = await (await page.request.get('/api/v1/config')).json()
  expect(cfg.system.management.firewallLog).toEqual({ days: 3 })
  expect(cfg.system.management.logDefaultDrops).toBe(true)

  // Put back, for the specs after this one.
  await page.goto('/firewall/log')
  await card.getByLabel('Days').fill('')
  await card.getByLabel('Log dropped packets').uncheck()
  await applyAndConfirm(page)
})

/**
 * Stands in for the router's firewall log: this server is not root, so its
 * own log stays empty. A thousand packets, read a page at a time and
 * searched on the "router", the way internal/fwlog answers.
 */
async function standInLog(page) {
  const held = Array.from({ length: 1000 }, (_, i) => ({
    seq: 1000 - i,
    time: new Date(Date.UTC(2026, 8, 26, 12, 0, 0) - i * 1000).toISOString(),
    kind: 'rule',
    ruleId: `r${1000 - i}`,
    action: i % 2 ? 'drop' : 'accept',
    in: 'eth0',
    family: 'ipv4',
    proto: 'tcp',
    src: '203.0.113.9',
    srcPort: 40000,
    dst: '198.51.100.2',
    dstPort: 443,
    length: 60,
  }))
  const asked = []
  await page.route('**/api/v1/log/entries?*', async (route) => {
    const url = new URL(route.request().url())
    asked.push(url.searchParams)
    const before = Number(url.searchParams.get('before') ?? 0)
    const q = url.searchParams.get('q') ?? ''
    const limit = Number(url.searchParams.get('limit'))
    const found = held.filter((e) => (!before || e.seq < before) && (!q || e.ruleId === q))
    const entries = found.slice(0, limit)
    const more = found.length > limit
    await route.fulfill({
      json: {
        entries,
        more,
        next: more ? entries.at(-1).seq : undefined,
        held: 1000,
        oldest: held.at(-1).time,
      },
    })
  })
  // No stream: a 204 tells the browser not to try again.
  await page.route('**/api/v1/log/stream', (route) => route.fulfill({ status: 204 }))
  return asked
}

test('the log reads on as it scrolls, and searches on the router', async ({ page }) => {
  const asked = await standInLog(page)
  await login(page)
  await page.goto('/firewall/log')
  const log = page.getByRole('region', { name: 'Logs' })
  const rows = log.locator('tbody tr')
  await expect(rows).toHaveCount(200)
  await expect(log).toContainText('1,000 entries back to')
  const live = log.getByRole('button', { name: 'Live' })
  await expect(live).toHaveAttribute('aria-pressed', 'true')

  // Scrolling to the foot reads the next page, and reading history stops Live.
  await log.getByRole('button', { name: 'Load more' }).scrollIntoViewIfNeeded()
  await expect(rows).toHaveCount(400)
  await expect(live).toHaveAttribute('aria-pressed', 'false')
  expect(asked.at(-1).get('before')).toBe('801')

  // The search goes to the router once typing rests.
  await log.getByRole('searchbox').fill('r7')
  await expect(rows).toHaveCount(1)
  await expect(rows.first()).toContainText('r7')
  expect(asked.at(-1).get('q')).toBe('r7')
  await expect(log).toContainText('Start of the log.')

  // Cleared, it is the plain log again.
  await log.getByRole('searchbox').fill('')
  await expect(rows).toHaveCount(200)
  await page.screenshot({ path: shot('27-firewall-log'), fullPage: true })
})
