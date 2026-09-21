import { expect, test } from '@playwright/test'

import { PASSWORD, applyAndConfirm, confirmDialog, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('system settings, ruleset view, and rollback via revisions', async ({ page }) => {
  await login(page)
  await page.goto('/system')
  await expect(page).toHaveURL(/\/system\/general$/)
  await expect(page.getByRole('heading', { name: 'General', exact: true, level: 1 })).toBeVisible()
  // Every router starts in UTC.
  await expect(page.getByLabel('Timezone')).toHaveValue('UTC')

  await page.getByLabel('Hostname').fill('edge2')
  await expect(page.getByText('Unapplied changes.')).toBeVisible()
  // The draft renders on its own page; moving in-app keeps it.
  await sidebar(page, 'Ruleset')
  await page.getByRole('button', { name: 'Render the draft' }).click()
  await expect(page.getByLabel('draft ruleset')).toContainText('table inet ostiole')
  await page.screenshot({ path: shot('30-system'), fullPage: true })
  await applyAndConfirm(page)

  // Roll back to the previous revision through the draft.
  await page.goto('/system/backup')
  const rows = page
    .getByRole('row')
    .filter({ has: page.getByRole('button', { name: 'Load into draft' }) })
  await expect(rows.first()).toBeVisible()
  await rows.first().getByRole('button', { name: 'Load into draft' }).click()
  await expect(page.getByRole('status')).toContainText('is now the draft')
  await sidebar(page, 'General')
  await expect(page.getByLabel('Hostname')).toHaveValue('edge')
  await expect(page.getByText('Unapplied changes.')).toBeVisible()
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByLabel('Hostname')).toHaveValue('edge2')

  await sidebar(page, 'Ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  await expect(page.getByLabel('confirmed ruleset')).toContainText('anti-lockout:lan')
})

test('change password and sign in with it', async ({ page }) => {
  await login(page)
  await page.goto('/system/accounts')
  await page.getByLabel('Current password').fill('nope nope nope')
  await page.getByLabel('New password', { exact: true }).fill('a completely new one')
  await page.getByLabel('Repeat new password').fill('a completely new one')
  await page.getByRole('button', { name: 'Change password' }).click()
  await expect(page.getByRole('alert')).toContainText('Current password is wrong')

  await page.getByLabel('Current password').fill(PASSWORD)
  await page.getByLabel('New password', { exact: true }).fill('a completely new one')
  await page.getByLabel('Repeat new password').fill('a completely new one')
  await page.getByRole('button', { name: 'Change password' }).click()
  await expect(page.getByText('Password changed.')).toBeVisible()

  // The session survived the change.
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Dashboard', level: 1 })).toBeVisible()

  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password', { exact: true }).fill('a completely new one')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/$/)

  // Restore the shared password for any later specs.
  await page.goto('/system/accounts')
  await page.getByLabel('Current password').fill('a completely new one')
  await page.getByLabel('New password', { exact: true }).fill(PASSWORD)
  await page.getByLabel('Repeat new password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Change password' }).click()
  await expect(page.getByText('Password changed.')).toBeVisible()
})

test('mint an API token and use it to scrape metrics', async ({ page, request }) => {
  await login(page)
  await page.goto('/system/accounts')
  const accounts = page.getByRole('region', { name: 'Accounts' })
  const section = page.getByRole('region', { name: 'API tokens' })
  await expect(accounts.getByRole('row').filter({ hasText: 'admin' })).toContainText('Admin')

  await section.getByRole('button', { name: 'Add token' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('monitoring')
  await dialog.getByLabel('Role', { exact: true }).selectOption('viewer')
  await dialog.getByRole('button', { name: 'Create token' }).click()

  const shown = section.getByRole('note')
  await expect(shown).toContainText('cannot be shown again')
  const secret = (await shown.locator('code').innerText()).trim()
  expect(secret).toMatch(/^ost_/)
  await page.screenshot({ path: shot('50-tokens'), fullPage: true })
  await shown.getByRole('button', { name: 'Close' }).click()

  // The token works on its own: no cookie, no CSRF header.
  const metrics = await request.get('/metrics', {
    headers: { Authorization: `Bearer ${secret}` },
  })
  expect(metrics.status()).toBe(200)
  expect(await metrics.text()).toContain('ostiole_build_info')

  // And it is a viewer, so it cannot change anything.
  const apply = await request.post('/api/v1/apply/confirm', {
    headers: { Authorization: `Bearer ${secret}` },
  })
  expect(apply.status()).toBe(403)

  // The description of the API is public and lists the roles.
  const spec = await (await request.get('/api/v1/openapi.json')).json()
  expect(spec.paths['/api/v1/apply'].post['x-required-role']).toBe('operator')

  const row = section.getByRole('row').filter({ hasText: 'monitoring' })
  await row.getByRole('button', { name: 'Delete' }).click()
  await confirmDialog(page, { typed: 'monitoring' })
  await expect(section.getByRole('row').filter({ hasText: 'monitoring' })).toHaveCount(0)
})

test('add an operator, rename it, prove what it may do, and remove it', async ({ page }) => {
  const OPERATOR_PASSWORD = 'a second account here'
  await login(page)
  await page.goto('/system/accounts')
  const section = page.getByRole('region', { name: 'Accounts' })

  // Your own row cannot be demoted or deleted, which is the only thing
  // standing between an admin and a router they can no longer manage.
  const own = section.getByRole('row').filter({ has: page.getByLabel('Role for admin') })
  await expect(own.getByLabel('Role for admin')).toBeDisabled()
  await expect(own.getByRole('button', { name: 'Delete' })).toHaveCount(0)

  await section.getByRole('button', { name: 'Add account' }).click()
  const add = page.getByRole('dialog')
  await add.getByLabel('Username').fill('opsy')
  await add.getByLabel('Password', { exact: true }).fill(OPERATOR_PASSWORD)
  await add.getByLabel('Role', { exact: true }).selectOption('operator')
  await add.getByRole('button', { name: 'Create account' }).click()
  // 'ops' is a prefix of 'opsy', so rows are matched by their exact role
  // label: a row on its way out would otherwise answer for the new one.
  const roleOf = (name) => page.getByLabel(`Role for ${name}`, { exact: true })
  const rowOf = (name) => section.getByRole('row').filter({ has: roleOf(name) })
  await expect(roleOf('opsy')).toHaveValue('operator')
  await page.screenshot({ path: shot('51-accounts'), fullPage: true })

  await rowOf('opsy').getByRole('button', { name: 'Rename' }).click()
  const rename = page.getByRole('dialog')
  await rename.getByLabel('New username').fill('ops')
  await rename.getByRole('button', { name: 'Rename' }).click()
  await expect(roleOf('opsy')).toHaveCount(0)
  await expect(roleOf('ops')).toHaveValue('operator')

  // The new account is real, and its role is the whole of what it may do:
  // an operator applies configuration but never sees this section.
  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.getByLabel('Username').fill('ops')
  await page.getByLabel('Password', { exact: true }).fill(OPERATOR_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/$/)
  await page.goto('/system/accounts')
  await expect(page.getByRole('region', { name: 'Accounts' })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Change password' })).toBeVisible()
  await page.getByRole('button', { name: 'Sign out' }).click()

  await login(page)
  await page.goto('/system/accounts')
  await rowOf('ops').getByRole('button', { name: 'Delete' }).click()
  await confirmDialog(page, { typed: 'ops' })
  await expect(roleOf('ops')).toHaveCount(0)
})

test('choose how this router patches itself, and see why some of it is refused', async ({
  page,
}) => {
  await login(page)
  await page.goto('/system/updates')

  const os = page.getByRole('region', { name: 'Operating system updates' })
  await expect(os).toContainText('dnf')
  // The end-to-end server is not root, so the card says so rather than
  // pretending it could install anything.
  await expect(os).toContainText('root')
  await expect(os.getByRole('button', { name: 'Check now' })).toBeDisabled()

  // The defaults are what a router gets without being told: security
  // fixes, asked for nightly and installed on a Sunday.
  await expect(os.getByLabel('Automatic (Security)')).toBeChecked()
  await expect(os.getByLabel('Check schedule')).toHaveValue('0 4 * * *')
  await expect(os.getByLabel('Install schedule')).toHaveValue('30 4 * * 0')

  await os.getByLabel('Automatic (All)').check()
  await os.getByLabel('Install automatically').selectOption('0 4 * * 0')
  // A glob is a legitimate entry, and has to survive validation on apply.
  await os.getByLabel('Never upgrade').fill('kernel*, dkms')

  const ostiole = page.getByRole('region', { name: 'Ostiole updates' })
  await ostiole.getByLabel('Manual').check()
  // A router that installs Ostiole by hand has no install to schedule;
  // it still asks what is waiting.
  await expect(ostiole.getByLabel('Install schedule')).toHaveCount(0)
  await expect(ostiole.getByLabel('Check schedule')).toHaveValue('0 4 * * *')
  await ostiole.getByLabel('Channel').selectOption('beta')

  await page.screenshot({ path: shot('31-updates'), fullPage: true })
  await applyAndConfirm(page)

  // Applied, so the daemon is working to these settings and the page
  // shows them after a reload.
  await page.reload()
  await expect(os.getByLabel('Automatic (All)')).toBeChecked()
  await expect(os.getByLabel('Check schedule')).toHaveValue('0 4 * * *')
  await expect(os.getByLabel('Install schedule')).toHaveValue('0 4 * * 0')
  await expect(os.getByLabel('Never upgrade')).toHaveValue('kernel*, dkms')
  await expect(ostiole.getByLabel('Manual')).toBeChecked()
  await expect(ostiole.getByLabel('Channel')).toHaveValue('beta')

  // All four update crons are on the page that says what the router does
  // by itself, and the one the manual mode turned off says so.
  await page.goto('/system/crons')
  const system = page.getByRole('region', { name: "Ostiole's crons" })
  const row = (text) => system.getByRole('row').filter({ hasText: text })
  await expect(row('what updates are waiting')).toContainText('0 4 * * *')
  await expect(row('Install the distro updates')).toContainText('0 4 * * 0')
  await expect(row('newer Ostiole release has been published')).toContainText('0 4 * * *')
  await expect(row('Install a newer Ostiole release')).toContainText('never')
})
