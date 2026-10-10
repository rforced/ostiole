import { expect, test } from './fixtures.js'
import { applyAndConfirm, confirmDialog, login, readConfig } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// History says who applied each configuration and the audit log what each
// account did. Only an admin sees who or from where.

const headers = { 'X-Requested-With': 'ostiole' }
const OPERATOR = { username: 'bob', password: 'an operator of our own' }

/** A session of its own for an account, through the API. */
async function signedIn(playwright, baseURL, username, password) {
  const api = await playwright.request.newContext({ baseURL })
  const res = await api.post('/api/v1/auth/login', { data: { username, password }, headers })
  expect(res.ok(), await res.text()).toBe(true)
  return api
}

test('History names who applied each configuration and who confirmed it', async ({
  page,
  playwright,
  baseURL,
}) => {
  await login(page)
  const created = await page.request.post('/api/v1/users', {
    data: { ...OPERATOR, role: 'operator' },
    headers,
  })
  expect(created.ok(), await created.text()).toBe(true)

  // The admin applies one change from the UI and confirms it.
  await page.goto('/system/general')
  await page.getByLabel('Hostname', { exact: true }).fill('edge-audited')
  await applyAndConfirm(page)

  // The operator applies the next one with a window; the admin confirms it.
  const bob = await signedIn(playwright, baseURL, OPERATOR.username, OPERATOR.password)
  const { config, baseRevision } = await readConfig(bob)
  config.system.hostname = 'edge-by-bob'
  const applied = await bob.post('/api/v1/apply', {
    data: { config, confirmTimeoutSeconds: 60, baseRevision },
    headers,
  })
  expect(applied.ok(), await applied.text()).toBe(true)
  const confirmed = await page.request.post('/api/v1/apply/confirm', { headers })
  expect(confirmed.ok(), await confirmed.text()).toBe(true)

  await page.goto('/system/configuration')
  const history = page.getByRole('region', { name: 'Revisions' })
  await expect(
    history.getByText(/In force since .*, applied by bob .*confirmed by admin/),
  ).toBeVisible()
  // The configuration bob replaced was the admin's.
  await expect(history.getByRole('row').nth(1)).toContainText('admin')

  // An operator reads History but not who made it.
  const asBob = await bob.get('/api/v1/config/revisions')
  expect(asBob.ok()).toBe(true)
  for (const r of await asBob.json()) expect(r.applied).toBeUndefined()
  expect((await bob.get('/api/v1/config/applied')).status()).toBe(403)
  expect((await bob.get('/api/v1/audit')).status()).toBe(403)
  await bob.dispose()
})

test('the audit log lists what each account did, and a Clear leaves who cleared it', async ({
  page,
}) => {
  await login(page)
  await page.goto('/system/accounts')
  await page.getByRole('tab', { name: 'Audit log' }).click()
  const log = page.getByRole('region', { name: 'Audit log' })
  const rows = log.getByRole('row')
  await expect(rows.filter({ hasText: 'Created the account bob as operator' })).toHaveCount(1)
  await expect(rows.filter({ hasText: "Confirmed bob's apply" })).toHaveCount(1)
  await expect(
    rows.filter({ hasText: 'bob' }).filter({ hasText: 'Applied the configuration' }),
  ).toHaveCount(1)
  await expect(rows.filter({ hasText: 'Signed in' }).first()).toBeVisible()

  // A search keeps the rows holding every word.
  await log.getByRole('searchbox').fill('created bob')
  await expect(rows.filter({ hasText: 'Created the account bob' })).toHaveCount(1)
  await expect(rows.filter({ hasText: 'Signed in' })).toHaveCount(0)
  await log.getByRole('searchbox').fill('')
  await expect(rows.filter({ hasText: 'Signed in' }).first()).toBeVisible()

  await log.getByRole('button', { name: 'Clear', exact: true }).click()
  await confirmDialog(page, { confirm: 'Clear' })
  await expect(rows.filter({ hasText: 'Cleared the audit log' })).toHaveCount(1)
  await expect(rows.filter({ hasText: 'Signed in' })).toHaveCount(0)
})

test('an operator sees neither the audit log nor who applied', async ({ page }) => {
  await page.goto('/login')
  await page.getByLabel('Username').fill(OPERATOR.username)
  await page.getByLabel('Password', { exact: true }).fill(OPERATOR.password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/$/)

  await page.goto('/system/accounts')
  await expect(page.getByRole('heading', { name: 'Change password' })).toBeVisible()
  await expect(page.getByRole('tab', { name: 'Audit log' })).toHaveCount(0)

  await page.goto('/system/configuration')
  const history = page.getByRole('region', { name: 'Revisions' })
  await expect(history.getByRole('row').nth(1)).toBeVisible()
  await expect(history.getByRole('columnheader', { name: 'Applied by' })).toHaveCount(0)
  await expect(history.getByText(/In force since/)).toHaveCount(0)
})

test('a revert names who asked for it', async ({ page }) => {
  await login(page)
  const { config, baseRevision } = await readConfig(page.request)
  config.system.hostname = 'not-for-keeps'
  const applied = await page.request.post('/api/v1/apply', {
    data: { config, confirmTimeoutSeconds: 60, baseRevision },
    headers,
  })
  expect(applied.ok(), await applied.text()).toBe(true)
  await page.goto('/')
  const status = page.getByRole('status')
  await status.getByRole('button', { name: 'Revert now' }).click()
  await expect(status).toContainText('Reverted to the previous configuration.')

  const res = await page.request.get('/api/v1/audit?q=reverted')
  expect(res.ok()).toBe(true)
  const { entries } = await res.json()
  expect(entries[0].text).toBe('Reverted their apply')
  expect(entries[0].by).toMatchObject({ name: 'admin', kind: 'account', role: 'admin' })
})
