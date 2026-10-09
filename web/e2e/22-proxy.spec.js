import { expect, test } from './fixtures.js'
import { applyAndConfirm, confirmDialog, login, reconfigure, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('the page is honest about the sidecar not being installed', async ({ page }) => {
  await login(page)
  await page.goto('/services/proxy')
  await expect(
    page.getByRole('heading', { name: 'Reverse proxy', exact: true, level: 1 }),
  ).toBeVisible()
  // The e2e server is not root and has no ostiole-proxy, so the strip says
  // so rather than the page claiming the proxy runs.
  await expect(page.getByRole('note')).toContainText('Not on this router')
  await expect(page.getByRole('note')).toContainText('ostiole repair --proxy')
})

test('publish a site through a pool and apply it', async ({ page }) => {
  await login(page)
  // The access list admits an alias of admins, as 03-firewall.spec.js
  // writes one.
  await reconfigure(page.request, (config) => {
    config.aliases = [
      ...(config.aliases ?? []),
      { name: 'admins', type: 'hosts', entries: ['203.0.113.10', '2001:db8::10'] },
    ]
  })
  await page.goto('/services/proxy')

  // Ports of its own, so nothing else in the suite has to move.
  await page.getByLabel('Reverse proxy enabled').check()
  await page.getByRole('spinbutton', { name: 'HTTP port' }).fill('8080')
  await page.getByRole('spinbutton', { name: 'HTTPS port' }).fill('8443')
  await expect(page.getByText('Nothing reaches the proxy until a rule accepts it.')).toBeVisible()

  await page.getByRole('tab', { name: 'Pools' }).click()
  await page.getByRole('button', { name: 'Add pool' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('web')
  await dialog.getByRole('textbox', { name: 'Upstreams' }).fill('192.168.50.20:8080')
  await dialog.getByLabel('Health path').fill('/healthz')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'web' })).toContainText('192.168.50.20:8080')

  await page.getByRole('tab', { name: 'WAF' }).click()
  await page.getByRole('button', { name: 'Add profile' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('watch')
  await dialog.getByLabel('wordpress').check()
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const profile = page.getByRole('row').filter({ hasText: 'watch' })
  await expect(profile).toContainText('detect only')
  await expect(profile).toContainText('wordpress')

  await page.getByRole('tab', { name: 'Sites' }).click()
  await page.getByRole('button', { name: 'Add site' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('shop')
  await dialog.getByLabel('Hostnames').fill('shop.example.com')
  await dialog.getByLabel('WAF profile').selectOption('watch')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const site = page.getByRole('row').filter({ hasText: 'shop' })
  await expect(site).toContainText('shop.example.com')
  await expect(site).toContainText('built-in')

  await page.getByRole('tab', { name: 'Routes' }).click()
  await page.getByRole('button', { name: 'Add route' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('imap')
  await dialog.getByRole('spinbutton', { name: 'Port' }).fill('993')
  await dialog.getByRole('textbox', { name: 'Upstreams' }).fill('192.168.50.30:993')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const imap = page.getByRole('row').filter({ hasText: 'imap' })
  await expect(imap).toContainText('tcp/993')
  // A new route is reachable nowhere until an access rule names it.
  await expect(imap).toContainText('No rule opens it')
  await page.screenshot({ path: shot('110-proxy-routes'), fullPage: true })

  // HTTPS on wan from the admins alias, then HTTP and the route from
  // anywhere, moved above it.
  await page.getByRole('tab', { name: 'Service' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Admins')
  await dialog.getByLabel('Zone').selectOption('wan')
  await dialog.getByLabel('Match', { exact: true }).selectOption('alias')
  await dialog.getByLabel('Alias', { exact: true }).selectOption('admins')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Mail and redirects')
  await dialog.getByRole('checkbox', { name: /^HTTPS / }).uncheck()
  await dialog.getByRole('checkbox', { name: /^HTTP / }).check()
  await expect(
    dialog.getByText('Sites send HTTP to HTTPS unless they serve plain HTTP.'),
  ).toBeVisible()
  await dialog.getByRole('checkbox', { name: /^imap / }).check()
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const access = page.getByRole('region', { name: 'Access' }).getByRole('row')
  await expect(access.nth(1)).toContainText('Admins')
  await expect(access.nth(1)).toContainText('@admins')
  await expect(access.nth(2)).toContainText('HTTP, imap')
  await page.getByRole('button', { name: 'Move Mail and redirects up' }).click()
  await expect(access.nth(1)).toContainText('Mail and redirects')
  await page.screenshot({ path: shot('111-proxy-access'), fullPage: true })
  await page.getByRole('tab', { name: 'Routes' }).click()
  await expect(imap).toContainText('wan')

  await applyAndConfirm(page)

  // Each rule opens its ports at the tail of the zone's chain, in order.
  const ruleset = await page.evaluate(async () => {
    const res = await fetch('/api/v1/ruleset', { headers: { 'X-Requested-With': 'ostiole' } })
    return await res.text()
  })
  const open = ruleset.indexOf(
    'fib daddr type local tcp dport { 993, 8080 } counter accept comment "proxy:',
  )
  const admins = ruleset.indexOf(
    'ip saddr @alias_admins_v4 fib daddr type local tcp dport 8443 counter accept comment "proxy:',
  )
  expect(open).toBeGreaterThan(0)
  expect(admins).toBeGreaterThan(open)

  // The Rules page shows them among the zone's own and sends an edit here.
  await page.goto('/firewall/rules#wan')
  const row = page.getByRole('row').filter({ hasText: 'Reverse proxy access: Admins' })
  await expect(row).toContainText('@admins')
  await expect(row).toContainText('this router : 8443')
  await row.getByRole('link', { name: 'Edit' }).click()
  await expect(page).toHaveURL(/\/services\/proxy#service$/)
})

test('exclude a rule on a path cut down from an event, then edit and delete one', async ({
  page,
}) => {
  // The e2e server is not root and reads no journal, so the event is a
  // stand-in, as the query log's are.
  await page.route('**/api/v1/proxy/events/stream', (route) => route.fulfill({ status: 204 }))
  await page.route(/\/api\/v1\/proxy\/events(\?.*)?$/, (route) =>
    route.fulfill({
      json: {
        held: 1,
        entries: [
          {
            seq: 1,
            logged: '2026-09-27T21:02:11Z',
            time: '2026-09-27T21:02:11Z',
            id: 'XmQ1',
            site: 'shop',
            client: '192.168.1.55',
            method: 'GET',
            uri: '/Items/1b2c3d4e/PlaybackInfo?UserId=abc',
            status: 200,
            verdict: 'would-block',
            engine: 'DetectionOnly',
            rules: [{ id: 942190, message: 'Detects MSSQL code execution', severity: 'critical' }],
          },
        ],
      },
    }),
  )
  await login(page)
  await page.goto('/services/proxy#events')
  await page.getByRole('button', { name: 'Exclude on this path' }).click()
  let dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Path', { exact: true })).toHaveValue(
    '/Items/1b2c3d4e/PlaybackInfo',
  )
  await dialog.getByLabel('Path', { exact: true }).fill('/Items')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByText('Excluded rule 942190 on /Items in the draft.')).toBeVisible()

  await page.getByRole('tab', { name: 'WAF' }).click()
  const card = page.getByRole('region', { name: 'Exclusions' })
  const fromEvent = card.getByRole('row').filter({ hasText: '942190' })
  await expect(fromEvent).toContainText('Detects MSSQL code execution')
  await expect(fromEvent).toContainText('/Items')
  await expect(fromEvent).toContainText('every variable')

  await card.getByRole('button', { name: 'Add exclusion' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Rule', { exact: true }).fill('920420')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const typed = card.getByRole('row').filter({ hasText: '920420' })
  await expect(typed).toContainText('every path')

  await typed.getByRole('button', { name: 'Edit' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Variable', { exact: true }).fill('args:q')
  await expect(dialog.getByRole('alert')).toHaveText(
    'A variable is written like ARGS or ARGS:password.',
  )
  await expect(dialog.getByRole('button', { name: 'Save to draft' })).toBeDisabled()
  await dialog.getByLabel('Variable', { exact: true }).fill('ARGS:q')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(typed).toContainText('ARGS:q')
  // Rule order: the one typed sorts ahead of the one from the event.
  await expect(card.getByRole('row').nth(1)).toContainText('920420')
  await page.screenshot({ path: shot('112-proxy-exclusions'), fullPage: true })

  await typed.getByRole('button', { name: 'Delete' }).click()
  await confirmDialog(page)
  await expect(typed).toHaveCount(0)
  await expect(fromEvent).toHaveCount(1)
})

// The e2e server keeps a WAF log that nothing feeds. Clear empties it on
// the router all the same, and says the journal keeps its own copy.
test('clear the WAF events', async ({ page }) => {
  await login(page)
  await page.goto('/services/proxy#events')
  const events = page.getByRole('region', { name: 'Events', exact: true })
  await expect(events).toContainText('No events.')
  await events.getByRole('button', { name: 'Clear', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Clear the WAF events?')
  await expect(dialog).toContainText('The journal keeps its own copy.')
  const cleared = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/proxy/events') && r.request().method() === 'DELETE',
  )
  await confirmDialog(page, { confirm: 'Clear' })
  expect((await cleared).status()).toBe(200)
  await expect(events).toContainText('No events.')
})

test('a reload comes back to the tab it was on', async ({ page }) => {
  await login(page)
  await page.goto('/services/proxy#sites')
  await expect(page.getByRole('tab', { name: 'Sites' })).toHaveAttribute('aria-selected', 'true')
  await page.reload()
  await expect(page).toHaveURL(/#sites$/)
  await expect(page.getByRole('row').filter({ hasText: 'shop' })).toBeVisible()
})

test('deleting a site asks for its name back', async ({ page }) => {
  await login(page)
  await page.goto('/services/proxy#sites')
  await page
    .getByRole('row')
    .filter({ hasText: 'shop' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await confirmDialog(page, { typed: 'shop' })
  await expect(page.getByText('No sites.')).toBeVisible()
  await applyAndConfirm(page)
})
