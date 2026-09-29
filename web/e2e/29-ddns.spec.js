import { createServer } from 'node:http'

import { expect, test } from './fixtures.js'
import { applyAndConfirm, confirmDialog, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// A stand-in for Cloudflare's API, where serve.sh points dynamic DNS. It
// holds records in memory and answers as the API did when it was measured:
// an envelope around everything, an exact name filter that ignores case.
const PORT = 18097
const TOKEN = 'e2e-dns-token'
const records = []
const asked = []
let api

function reply(res, status, result, errors = []) {
  res.writeHead(status, { 'Content-Type': 'application/json' })
  res.end(JSON.stringify({ success: status < 300, errors, messages: [], result }))
}

test.beforeAll(async () => {
  api = createServer((req, res) => {
    let body = ''
    req.on('data', (chunk) => (body += chunk))
    req.on('end', () => {
      const url = new URL(req.url, `http://127.0.0.1:${PORT}`)
      const sent = body ? JSON.parse(body) : null
      asked.push({ method: req.method, path: url.pathname, query: url.searchParams, body: sent })
      if (req.headers.authorization !== `Bearer ${TOKEN}`) {
        reply(res, 403, null, [
          { code: 9109, message: 'Unauthorized to access requested resource' },
        ])
        return
      }
      if (req.method === 'GET' && url.pathname === '/zones') {
        // A name narrows the list to that zone; without one it is every zone.
        const name = url.searchParams.get('name') ?? 'example.com'
        reply(res, 200, name === 'example.com' ? [{ id: 'zone1', name }] : [])
        return
      }
      if (url.pathname === '/zones/zone1/dns_records') {
        if (req.method === 'GET') {
          const name = url.searchParams.get('name')?.toLowerCase()
          const type = url.searchParams.get('type')
          reply(
            res,
            200,
            records.filter((r) => (!name || r.name === name) && (!type || r.type === type)),
          )
          return
        }
        if (req.method === 'POST') {
          const record = { id: `rec${records.length + 1}`, ...sent }
          records.push(record)
          reply(res, 200, record)
          return
        }
      }
      reply(res, 404, null, [{ code: 7003, message: 'Could not route to that path' }])
    })
  })
  await new Promise((resolve) => api.listen(PORT, '127.0.0.1', resolve))
})

test.afterAll(() => api?.close())

// The WAN's address decides what can be published: the netns the suite
// runs in locally gives it 192.0.2.10, which counts as public, and CI's
// runner a private one. Either way the record is checked against the
// provider, applied, and never deleted there.
test('keep a record at the provider that holds its domain', async ({ page }) => {
  await login(page)
  await page.goto('/system/dns-providers')
  await page.getByRole('button', { name: 'Add provider' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('cf')
  await dialog.getByLabel('Kind').selectOption('cloudflare')
  await dialog.getByLabel('Domains').fill('example.com')
  await dialog.getByLabel('API token').fill('not-the-token')
  // Test tries the credentials and writes nothing.
  await dialog.getByRole('button', { name: 'Test' }).click()
  await expect(dialog.getByRole('alert')).toContainText('Cloudflare refused the token')
  await dialog.getByLabel('API token').fill(TOKEN)
  await dialog.getByRole('button', { name: 'Test' }).click()
  const tried = dialog.getByRole('status')
  await expect(tried).toContainText('Cloudflare accepts the credentials. They see example.com.')
  await expect(tried).toContainText('found.')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'example.com' })).toContainText('Cloudflare')

  // In the app, not page.goto: a full load would throw the draft away.
  await sidebar(page, 'Services', 'Dynamic DNS')
  await expect(page.getByRole('heading', { name: 'Dynamic DNS' })).toBeVisible()
  await page.getByRole('button', { name: 'Add record' }).click()
  dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Anyone can look this name up.')
  await dialog.getByLabel('Name').fill('home.example.com')
  await expect(dialog).toContainText('Written through cf (Cloudflare).')
  // The first link, the LAN: in the netns it carries 192.0.2.10, on CI's
  // runner a private address.
  await dialog.getByLabel('Interface').selectOption('eth0')

  // Check reads the provider with the draft's token and writes nothing.
  await dialog.getByRole('button', { name: 'Check' }).click()
  const found = dialog.getByRole('status')
  await expect(found).toContainText('Cloudflare')
  const said = (await found.textContent()).replaceAll('\u00a0', ' ')
  const created = /The apply creates it with (\S+)\./.exec(said)
  const address = created?.[1]
  if (!address) expect(said).toContain('has no public IPv4 address')
  expect(asked.some((a) => a.path === '/zones' && a.query.get('name') === 'example.com')).toBe(true)
  expect(asked.filter((a) => a.method !== 'GET')).toEqual([])
  await page.screenshot({ path: shot('125-ddns-dialog'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  await applyAndConfirm(page)
  const row = page.getByRole('row').filter({ hasText: 'home.example.com' })
  // The updater reads the new record within a second of the apply.
  const settled = { timeout: 15_000 }
  if (address) {
    await expect(row).toContainText('current', settled)
    expect(records).toEqual([
      expect.objectContaining({
        type: 'A',
        name: 'home.example.com',
        content: address,
        ttl: 1,
        proxied: false,
        comment: 'Dynamic DNS from Ostiole',
      }),
    ])
    // Update now reads the record again, and finds nothing to change.
    const reads = () => asked.filter((a) => a.path === '/zones/zone1/dns_records').length
    const before = reads()
    await row.getByRole('button', { name: 'Update now' }).click()
    await expect.poll(reads, settled).toBeGreaterThan(before)
    await expect(row).toContainText('current', settled)
    expect(asked.filter((a) => a.method !== 'GET')).toHaveLength(1)
  } else {
    await expect(row).toContainText('no address', settled)
    expect(records).toEqual([])
  }
  await page.screenshot({ path: shot('126-ddns'), fullPage: true })

  // Deleting it here leaves it at the provider.
  await row.getByRole('button', { name: 'Delete' }).click()
  await expect(page.getByRole('dialog')).toContainText('The record at Cloudflare stays as it is.')
  await confirmDialog(page)
  await sidebar(page, 'System', 'DNS providers')
  await page
    .getByRole('row')
    .filter({ hasText: 'example.com' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await confirmDialog(page, { typed: 'cf' })
  await applyAndConfirm(page)
  expect(asked.filter((a) => a.method === 'DELETE')).toEqual([])
  const cfg = await (await page.request.get('/api/v1/config')).json()
  expect(cfg.services.ddns).toBeUndefined()
  expect(cfg.dnsProviders).toBeUndefined()
})

// The work is on the page that lists what the router does by itself.
test('the cron page lists the dynamic DNS updater', async ({ page }) => {
  await login(page)
  await page.goto('/system/crons')
  await expect(
    page.getByRole('region', { name: "Ostiole's cron jobs" }).getByRole('row').filter({
      hasText: 'Check dynamic DNS records',
    }),
  ).toContainText('every 1h')
})
