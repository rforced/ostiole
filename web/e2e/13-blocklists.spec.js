import { createServer } from 'node:http'

import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// A local stand-in for a published blocklist, for a cloud's address ranges
// in JSON, shaped like Oracle's, and for RIPEstat, so the test never
// reaches out to the internet.
let lists
let base
let listURL
let rangesURL
let served = 0

// What two documentation AS numbers announce, and who holds them. The
// second announces nothing, which is an answer and not a failure.
const announced = { 64500: ['203.0.113.0/25', '2001:db8:fe::/48'], 64501: [] }
const holders = { 64500: 'Example Networks', 64501: 'Quiet Networks' }

/** RIPEstat's answer: the data asked about, under "data". */
function ripestat(res, data) {
  res.writeHead(200, { 'Content-Type': 'application/json' })
  res.end(JSON.stringify({ status: 'ok', data }))
}

const ranges = {
  last_updated_timestamp: '2026-08-25T08:06:24.590745',
  regions: [
    {
      region: 'us-ashburn-1',
      cidrs: [{ cidr: '192.0.2.0/24', tags: ['OCI'] }],
      ipv6_cidrs: [{ cidr: '2001:db8:a::/48', tags: ['OCI'] }],
    },
    {
      region: 'eu-frankfurt-1',
      cidrs: [
        { cidr: '198.51.100.0/24', tags: ['OCI'] },
        { cidr: '203.0.113.0/24', tags: ['OSN'] },
      ],
      ipv6_cidrs: [],
    },
  ],
}

test.beforeAll(async () => {
  lists = createServer((req, res) => {
    served++
    const url = new URL(req.url, base)
    const resource = url.searchParams.get('resource') ?? ''
    if (url.pathname === '/announced-prefixes') {
      const prefixes = announced[resource.replace(/^AS/, '')] ?? []
      ripestat(res, { prefixes: prefixes.map((prefix) => ({ prefix })) })
      return
    }
    if (url.pathname === '/as-names') {
      const asked = resource.split(',').filter((n) => holders[n])
      ripestat(res, { names: Object.fromEntries(asked.map((n) => [n, holders[n]])) })
      return
    }
    if (req.url === '/public_ip_ranges.json') {
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify(ranges, null, 4))
      return
    }
    res.writeHead(200, { 'Content-Type': 'text/plain' })
    res.end('# a list\n192.0.2.0/24 ; note\n198.51.100.7\n2001:db8:dead::/48\n')
  })
  await new Promise((resolve) => lists.listen(0, '127.0.0.1', resolve))
  base = `http://127.0.0.1:${lists.address().port}`
  listURL = `${base}/drop.txt`
  rangesURL = `${base}/public_ip_ranges.json`
})

test.afterAll(() => lists?.close())

test('a blocklist alias fetches, lands in the ruleset, and refreshes', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await sidebar(page, 'Aliases')

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
  await sidebar(page, 'Rules')
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
  await page.goto('/system/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.locator('pre')
  await expect(ruleset).toContainText('set alias_blocklist_v4')
  await expect(ruleset).toContainText('set alias_blocklist_v6')
  await expect(ruleset).toContainText('@alias_blocklist_v6')

  // Fetch it, and the cached entries show up against the alias.
  await page.goto('/firewall')
  await sidebar(page, 'Aliases')
  const listed = page.getByRole('row').filter({ hasText: 'blocklist' })
  await listed.getByRole('button', { name: 'Refresh' }).click()
  // Three from the list; the entry typed in by hand is not "fetched".
  await expect(listed).toContainText('3 fetched')
  expect(served).toBeGreaterThan(0)
  await page.screenshot({ path: shot('99-blocklist-fetched'), fullPage: true })
})

// The router reads a JSON list as soon as its URL is typed, offers what it
// can be narrowed by, and keeps only what was ticked.
test('a JSON list is narrowed to one region', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await sidebar(page, 'Aliases')

  await page.getByRole('button', { name: 'Add alias' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('cloud')
  await expect(dialog.getByText('Keep only')).toHaveCount(0)
  await dialog.getByLabel('Fetch from').fill(rangesURL)
  await dialog.getByLabel('Fetch from').blur()
  await dialog.getByRole('checkbox', { name: 'us-ashburn-1' }).check()
  await expect(dialog).toContainText('1 ticked: region=us-ashburn-1')
  await page.screenshot({ path: shot('99-json-list'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const row = page.getByRole('row').filter({ hasText: 'cloud' })
  await expect(row).toContainText('Keeps only region=us-ashburn-1')
  await applyAndConfirm(page)

  // One prefix of each family from that region, of the four in the list.
  await row.getByRole('button', { name: 'Refresh' }).click()
  await expect(row).toContainText('2 fetched')
})

// An AS alias expands through RIPEstat, whose URLs are settings with no
// page, as the GeoIP ones are: one call per AS, and one for the names.
test('an AS alias fetches what each network announces', async ({ page }) => {
  await login(page)
  const cfg = await (await page.request.get('/api/v1/config')).json()
  cfg.system.asnUrl = `${base}/announced-prefixes?resource=AS{asn}`
  cfg.system.asnNamesUrl = `${base}/as-names?resource={asns}`
  const headers = { 'X-Requested-With': 'ostiole' }
  const applied = await page.request.post('/api/v1/apply', {
    data: { config: cfg, confirmTimeoutSeconds: 60 },
    headers,
  })
  expect(applied.ok()).toBe(true)
  expect((await page.request.post('/api/v1/apply/confirm', { headers })).ok()).toBe(true)

  await page.goto('/firewall')
  await sidebar(page, 'Aliases')
  await page.getByRole('button', { name: 'Add alias' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('carriers')
  await dialog.getByLabel('Type').selectOption('asn')
  await expect(dialog.getByLabel('Fetch from')).toHaveCount(0)
  await dialog.getByLabel('Entries').fill('as64500, transit')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(dialog.getByRole('alert')).toHaveText('transit is not an AS number.')

  // Any spelling goes in, one comes out, and a repeat is dropped.
  await dialog.getByLabel('Entries').fill('as64500, 64501, AS64500')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const row = page.getByRole('row').filter({ hasText: 'carriers' })
  await expect(row).toContainText('AS64500, AS64501')
  await expect(row).toContainText('not fetched yet')
  await applyAndConfirm(page)

  // Both families from the first, nothing from the second, each named.
  await row.getByRole('button', { name: 'Refresh' }).click()
  await expect(row).toContainText('2 fetched')
  await expect(row).toContainText('AS64500 Example Networks, AS64501 Quiet Networks')
  await page.screenshot({ path: shot('99-asn-list'), fullPage: true })

  // The dialog counts by network, so the one that announces nothing shows.
  await row.getByRole('button', { name: 'Edit' }).click()
  const edit = page.getByRole('dialog')
  await expect(edit).toContainText('AS64500 · Example Networks · 2 prefixes')
  await expect(edit).toContainText('AS64501 · Quiet Networks · 0 prefixes')
  await edit.getByRole('button', { name: 'Cancel' }).click()
})

test('a country alias is picked by name, and a preset picks a whole bloc', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await sidebar(page, 'Aliases')

  await page.getByRole('button', { name: 'Add alias' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('countries')
  await dialog.getByLabel('Type').selectOption('geoip')
  // There is no URL to give: the source is a system setting.
  await expect(dialog.getByLabel('Fetch from')).toHaveCount(0)

  // Countries are chosen by name; the codes are what gets stored.
  await dialog.getByLabel('Search', { exact: true }).fill('german')
  await dialog.getByRole('checkbox', { name: /Germany/ }).check()
  await dialog.getByLabel('Search', { exact: true }).fill('france')
  await dialog.getByRole('checkbox', { name: /France/ }).check()
  await expect(dialog).toContainText('2 selected')
  await expect(dialog).toContainText('France, Germany')

  // One click for a bloc nobody wants to tick 27 times.
  await dialog.getByLabel('Search', { exact: true }).fill('')
  await dialog.getByRole('button', { name: '+ European Union' }).click()
  await expect(dialog).toContainText('27 selected')
  await page.screenshot({ path: shot('99-countries'), fullPage: true })

  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const row = page.getByRole('row').filter({ hasText: 'countries' })
  await expect(row).toContainText('27 countries')
  await expect(row).toContainText('Austria')

  // It is a real alias: a rule can use it, and the ruleset gets both families.
  await sidebar(page, 'Rules')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'wan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  const rule = page.getByRole('dialog')
  await rule.getByLabel('Description').fill('Block the EU')
  await rule.getByLabel('Action').selectOption('drop')
  await rule.getByLabel('Match', { exact: true }).first().selectOption('alias')
  await rule.getByLabel('Alias', { exact: true }).selectOption('countries')
  await rule.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'Block the EU' })).toContainText(
    '@countries',
  )

  await applyAndConfirm(page)
})

test('hybrid outbound NAT puts your rules ahead of the automatic one', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await sidebar(page, 'NAT')

  await page.getByLabel('Mode').selectOption('hybrid')
  await expect(page.getByText('behaves like automatic')).toBeVisible()

  // A host that keeps its own address on the way out.
  await page.getByRole('button', { name: 'Add outbound rule' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Mail server keeps its address')
  await dialog.getByLabel('Source networks').fill('192.168.50.25/32')
  await dialog.getByLabel('Leave as').fill('203.0.113.25')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  // And one that is kept out of NAT altogether.
  await page.getByRole('button', { name: 'Add outbound rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Routed to the partner network')
  await dialog.getByLabel('Source networks').fill('192.168.50.0/24')
  await dialog.getByLabel('Destination networks').fill('10.80.0.0/16')
  await dialog.getByLabel('Do not translate this traffic').check()
  // Naming an address makes no sense once nothing is translated.
  await expect(dialog.getByLabel('Leave as')).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const rows = page.getByRole('row')
  await expect(rows.filter({ hasText: 'keeps its address' })).toContainText('203.0.113.25')
  await expect(rows.filter({ hasText: 'partner network' })).toContainText('not translated')
  await page.screenshot({ path: shot('B0-hybrid-nat'), fullPage: true })

  await applyAndConfirm(page)

  // The rules land above the automatic masquerade, which is what makes
  // hybrid different from adding rules to automatic.
  await page.goto('/system/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const text = await page.locator('pre').innerText()
  const mail = text.indexOf('snat ip to 203.0.113.25')
  const noNat = text.indexOf('counter return')
  const auto = text.indexOf('auto-nat:')
  expect(mail).toBeGreaterThan(-1)
  expect(noNat).toBeGreaterThan(-1)
  expect(auto).toBeGreaterThan(mail)
  expect(auto).toBeGreaterThan(noNat)
})
