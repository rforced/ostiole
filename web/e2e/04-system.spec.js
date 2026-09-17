import { expect, test } from '@playwright/test'

import { PASSWORD, applyAndConfirm, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('system settings, ruleset view, and rollback via revisions', async ({ page }) => {
  await login(page)
  await page.goto('/system')
  await expect(page).toHaveURL(/\/system\/general$/)
  await expect(page.getByRole('heading', { name: 'General', exact: true })).toBeVisible()

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
  await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible()

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

test('the certificate section says there is nothing to manage without HTTPS', async ({ page }) => {
  await login(page)
  await page.goto('/system/general')
  const section = page.getByRole('region', { name: 'Certificate' })
  await expect(section).toContainText('not serving HTTPS')
  await expect(section.getByRole('button', { name: 'Regenerate self-signed' })).toHaveCount(0)
})

test('mint an API token and use it to scrape metrics', async ({ page, request }) => {
  await login(page)
  await page.goto('/system/accounts')
  const section = page.getByRole('region', { name: 'Accounts and API tokens' })
  await expect(section.getByRole('row').filter({ hasText: 'admin' })).toContainText('Admin')

  await section.getByRole('button', { name: 'New token' }).click()
  await section.getByLabel('Name').fill('monitoring')
  await section.getByLabel('Role', { exact: true }).selectOption('viewer')
  await section.getByRole('button', { name: 'Create' }).click()

  const shown = section.getByRole('note')
  await expect(shown).toContainText('cannot be shown again')
  const secret = (await shown.locator('code').innerText()).trim()
  expect(secret).toMatch(/^ost_/)
  await page.screenshot({ path: shot('50-tokens'), fullPage: true })
  await shown.getByRole('button', { name: 'Done' }).click()

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
  await row.getByRole('button', { name: /Delete\?/ }).click()
  await expect(section.getByRole('row').filter({ hasText: 'monitoring' })).toHaveCount(0)
})

test('choose how this box patches itself, and see why some of it is refused', async ({ page }) => {
  await login(page)
  await page.goto('/system/updates')

  const os = page.getByRole('region', { name: 'Operating system updates' })
  await expect(os).toContainText('dnf')
  // The end-to-end server is not root, so the card says so rather than
  // pretending it could install anything.
  await expect(os).toContainText('root')
  await expect(os.getByRole('button', { name: 'Check now' })).toBeDisabled()

  // The defaults are what a box gets without being told: security fixes,
  // Sunday at four.
  await expect(os.getByLabel('Automatic (Security)')).toBeChecked()
  await expect(os.getByLabel('Schedule', { exact: true })).toHaveValue('0 4 * * 0')

  await os.getByLabel('Automatic (All)').check()
  await os.getByLabel('When').selectOption('0 4 * * *')
  await os.getByLabel('Never upgrade').fill('kernel, kernel-core')

  const ostiole = page.getByRole('region', { name: 'Ostiole updates' })
  await ostiole.getByLabel('Manual').check()
  await ostiole.getByLabel('Channel').selectOption('beta')

  await page.screenshot({ path: shot('31-updates'), fullPage: true })
  await applyAndConfirm(page)

  // Applied, so the daemon is working to these settings and the page
  // shows them after a reload.
  await page.reload()
  await expect(os.getByLabel('Automatic (All)')).toBeChecked()
  await expect(os.getByLabel('Schedule', { exact: true })).toHaveValue('0 4 * * *')
  await expect(os.getByLabel('Never upgrade')).toHaveValue('kernel, kernel-core')
  await expect(ostiole.getByLabel('Manual')).toBeChecked()
  await expect(ostiole.getByLabel('Channel')).toHaveValue('beta')

  // Both jobs are on the page that says what the box does by itself.
  await page.goto('/crons')
  const system = page.getByRole('region', { name: 'What Ostiole does by itself' })
  await expect(system).toContainText('distro package manager')
  await expect(system).toContainText('newer Ostiole release')
  await expect(system.getByRole('row').filter({ hasText: 'distro package manager' })).toContainText(
    '0 4 * * *',
  )
})
