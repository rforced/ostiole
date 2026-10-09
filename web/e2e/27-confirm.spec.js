import { expect, test } from './fixtures.js'
import { login, readConfig, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// Every other spec confirms its applies. Here nobody does: Revert now puts
// the previous configuration back at once, and a window left to run out
// puts it back on its own, which is what saves a router whose change cut
// its operator off.

const headers = { 'X-Requested-With': 'ostiole' }

/** What the router has saved, which only a confirm changes. */
async function saved(page) {
  return (await page.request.get('/api/v1/config')).json()
}

async function pendingApply(page) {
  return (await (await page.request.get('/api/v1/status')).json()).pending ?? null
}

test('Revert now puts the previous rules back and keeps the change to fix', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'wan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Not for keeps')
  await dialog.getByLabel('Protocol').selectOption('udp')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const rule = page.getByRole('row').filter({ hasText: 'Not for keeps' })
  await expect(rule).toBeVisible()

  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  const status = page.getByRole('status')
  await expect(status).toContainText('awaiting confirmation')
  await expect(status.getByRole('timer')).toHaveText(/^\d+s$/)
  expect(await pendingApply(page)).not.toBeNull()
  await page.screenshot({ path: shot('96-apply-pending'), fullPage: true })

  await status.getByRole('button', { name: 'Revert now' }).click()
  await expect(status).toContainText('Reverted to the previous configuration.')
  expect(await pendingApply(page)).toBeNull()
  expect((await saved(page)).rules.map((r) => r.description)).not.toContain('Not for keeps')

  // The draft still holds the rule, to fix and apply again.
  await expect(rule).toBeVisible()
  await expect(page.getByText('Unapplied changes.')).toBeVisible()
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(rule).toHaveCount(0)
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})

// The window runs out while the page watches: it counts down, then says
// the previous configuration is back. The apply is made through the API,
// the one place a window shorter than the UI's minute can be asked for.
test('an apply nobody confirms is undone when its window runs out', async ({ page }) => {
  await login(page)
  const { config: before, baseRevision } = await readConfig(page.request)
  const cfg = structuredClone(before)
  cfg.system.hostname = 'left-to-run-out'
  const applied = await page.request.post('/api/v1/apply', {
    data: { config: cfg, confirmTimeoutSeconds: 10, baseRevision },
    headers,
  })
  expect(applied.ok()).toBe(true)

  await page.goto('/')
  const status = page.getByRole('status')
  await expect(status).toContainText('awaiting confirmation')
  const timer = status.getByRole('timer')
  const left = () => timer.textContent().then((t) => Number.parseInt(t, 10))
  const first = await left()
  expect(first).toBeLessThanOrEqual(10)
  await expect.poll(left).toBeLessThan(first)

  await expect(status).toContainText(
    'Not confirmed in time. The previous configuration was restored.',
    { timeout: 20_000 },
  )
  await page.screenshot({ path: shot('97-apply-expired'), fullPage: true })
  expect(await pendingApply(page)).toBeNull()
  expect((await saved(page)).system.hostname).toBe(before.system.hostname)
  // Nothing was changed in this tab, so there is nothing left to apply.
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})
