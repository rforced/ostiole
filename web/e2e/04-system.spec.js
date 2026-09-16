import { expect, test } from '@playwright/test'

import { PASSWORD, applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('system settings, ruleset view, and rollback via revisions', async ({ page }) => {
  await login(page)
  await page.goto('/system')
  await expect(page.getByRole('heading', { name: 'System', exact: true })).toBeVisible()

  await page.getByLabel('Hostname').fill('edge2')
  await expect(page.getByText('Unapplied changes.')).toBeVisible()
  await page.getByRole('button', { name: 'Render the draft' }).click()
  await expect(page.getByLabel('draft ruleset')).toContainText('table inet ostiole')
  await page.screenshot({ path: shot('30-system'), fullPage: true })
  await applyAndConfirm(page)

  // Roll back to the previous revision through the draft.
  await page.reload()
  const rows = page
    .getByRole('row')
    .filter({ has: page.getByRole('button', { name: 'Load into draft' }) })
  await expect(rows.first()).toBeVisible()
  await rows.first().getByRole('button', { name: 'Load into draft' }).click()
  await expect(page.getByRole('status')).toContainText('is now the draft')
  await expect(page.getByLabel('Hostname')).toHaveValue('edge')
  await expect(page.getByText('Unapplied changes.')).toBeVisible()
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByLabel('Hostname')).toHaveValue('edge2')

  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  await expect(page.getByLabel('confirmed ruleset')).toContainText('anti-lockout:lan')
})

test('change password and sign in with it', async ({ page }) => {
  await login(page)
  await page.goto('/system')
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
  await page.goto('/system')
  await page.getByLabel('Current password').fill('a completely new one')
  await page.getByLabel('New password', { exact: true }).fill(PASSWORD)
  await page.getByLabel('Repeat new password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Change password' }).click()
  await expect(page.getByText('Password changed.')).toBeVisible()
})

test('the certificate section says there is nothing to manage without HTTPS', async ({ page }) => {
  await login(page)
  await page.goto('/system')
  const section = page.getByRole('region', { name: 'Certificate' })
  await expect(section).toContainText('not serving HTTPS')
  await expect(section.getByRole('button', { name: 'Regenerate self-signed' })).toHaveCount(0)
})
