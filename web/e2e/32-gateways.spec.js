import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test } from './fixtures.js'
import { applyAndConfirm, confirmDialog, login, reconfigure, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// The server cannot probe, so its gateways answer as probes.json in its
// directory says, read again at every probe, and it probes every second.
// global-setup.js gave the WAN a next hop that answers.

/** Has addresses answer as said, beside the next hops the seed gave. */
function answers(server, addresses) {
  const file = join(server.dir, 'probes.json')
  const { hops } = JSON.parse(readFileSync(file, 'utf8'))
  writeFileSync(file, JSON.stringify({ hops, addresses }))
}

const card = (page, name) => page.getByRole('region', { name, exact: true })

async function wanGateway(page) {
  const config = await (await page.request.get('/api/v1/config')).json()
  return config.gateways[0]
}

function gatewayRow(page, name) {
  return card(page, 'Gateways').getByRole('row').filter({ hasText: name })
}

test("a new router's WAN is measured from the start", async ({ page }) => {
  await login(page)
  const wan = await wanGateway(page)
  expect(wan.name).toMatch(/^gw_/)
  await page.goto('/routing')
  const row = gatewayRow(page, wan.name)
  await expect(row.locator('.badge').first()).toHaveText('up', { timeout: 15_000 })
  await expect(row).toContainText('the next hop')

  await row.getByRole('button', { name: 'History' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Measured to the next hop.')
  await dialog.getByRole('combobox', { name: 'Window' }).selectOption('5m')
  await expect(dialog).toContainText('IPv4 1.0 ms', { timeout: 15_000 })
  await expect(dialog.locator('dl')).toContainText('1.0 ms mean')
  await page.screenshot({ path: shot('72-gateway-history'), fullPage: true })
  for (const w of ['24h', '31d']) {
    await dialog.getByRole('combobox', { name: 'Window' }).selectOption(w)
    await expect(dialog.getByRole('img')).toBeVisible()
  }
  await page.keyboard.press('Escape')
})

test('a gateway that stops answering goes down, and comes back', async ({ page, server }) => {
  await login(page)
  const wan = await wanGateway(page)
  await page.goto('/routing')
  const badge = gatewayRow(page, wan.name).locator('.badge').first()
  await expect(badge).toHaveText('up', { timeout: 15_000 })
  const events = card(page, 'Gateway events')

  answers(server, { '192.0.2.1': { silent: true } })
  await expect(badge).toHaveText('down', { timeout: 15_000 })
  await expect(events.getByRole('row').filter({ hasText: 'Down' })).toContainText(wan.name)

  answers(server, {})
  await expect(badge).toHaveText('up', { timeout: 15_000 })
  await expect(events.getByRole('row').filter({ hasText: 'Up again' })).toContainText(wan.name)
  await page.screenshot({ path: shot('73-gateway-events'), fullPage: true })
})

test('a gateway that never answered says so, until it has a monitor', async ({ page, server }) => {
  await login(page)
  const wan = await wanGateway(page)
  answers(server, { '198.51.100.9': { silent: true } })
  await reconfigure(page.request, (c) =>
    c.gateways.push({
      name: 'lte',
      enabled: true,
      interface: wan.interface,
      address: '198.51.100.9',
      priority: 1,
    }),
  )
  await page.goto('/routing')
  const row = gatewayRow(page, 'lte')
  await expect(row.locator('.badge').first()).toHaveText('never answered', { timeout: 15_000 })
  await expect(row).toContainText('Set a monitor address, or remove the gateway.')
  const events = card(page, 'Gateway events')
  await expect(events.getByRole('row').filter({ hasText: 'Never answered' })).toContainText('lte')

  await row.getByRole('button', { name: 'Edit' }).click()
  const dialog = page.getByRole('dialog')
  const monitor = dialog.getByLabel('Monitor address')
  await expect(monitor).toHaveAttribute('placeholder', 'the next hop')
  await dialog.getByRole('button', { name: 'Use 9.9.9.9, the DNS upstream' }).click()
  await expect(monitor).toHaveValue('9.9.9.9')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await applyAndConfirm(page)
  await expect(row.locator('.badge').first()).toHaveText('up', { timeout: 15_000 })
  await expect(row).toContainText('9.9.9.9')
  await expect(
    events.getByRole('row').filter({ hasText: 'Monitor changed to 9.9.9.9, was the next hop' }),
  ).toContainText('lte')
})

test("the gateways' history and events clear", async ({ page }) => {
  await login(page)
  await page.goto('/routing')
  const cleared = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/gateways/history') && r.request().method() === 'DELETE',
  )
  await card(page, 'Gateways').getByRole('button', { name: 'Clear', exact: true }).click()
  await expect(page.getByRole('dialog')).toContainText('The events stay.')
  await confirmDialog(page, { confirm: 'Clear' })
  expect((await cleared).status()).toBe(200)

  const events = card(page, 'Gateway events')
  await events.getByRole('button', { name: 'Clear', exact: true }).click()
  await confirmDialog(page, { confirm: 'Clear' })
  await expect(events).toContainText('No events yet.')
})

test('traffic per link clears', async ({ page }) => {
  await login(page)
  await page.goto('/traffic')
  const cleared = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/traffic/interfaces') && r.request().method() === 'DELETE',
  )
  await page.getByRole('button', { name: 'Clear', exact: true }).click()
  await expect(page.getByRole('dialog')).toContainText('Counting carries on.')
  await confirmDialog(page, { confirm: 'Clear' })
  expect((await cleared).status()).toBe(200)
})
