import type { Page } from '@playwright/test'

import { expect, test } from './fixtures'
import { PASSWORD, confirmDialog, enterUsername, signOut } from './helpers'

// The Account page holds what an account does for itself: its password,
// MFA, its own API tokens and the browsers signed in as it. The menu at the
// foot of the sidebar opens it.

const headers = { 'X-Requested-With': 'ostiole' }
const SHOTS = '/var/tmp/account-plan/shots'
const WIDE = { width: 1440, height: 900 }

async function signIn(page: Page, username: string, password = PASSWORD) {
  await page.goto('/login')
  await enterUsername(page, username)
  await page.getByLabel('Password', { exact: true }).fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/$/)
}

const region = (page: Page, name: string) => page.getByRole('region', { name, exact: true })

test('the account menu opens the Account page', async ({ page }) => {
  await page.setViewportSize(WIDE)
  await signIn(page, 'admin')
  await page.getByRole('button', { name: 'Account menu' }).click()
  const items = page.getByRole('menuitem')
  await expect(items).toHaveText(['Account', 'Sign out'])
  // A full-page capture resizes the page, which closes the menu.
  await page.screenshot({ path: `${SHOTS}/user-menu.png` })

  await items.filter({ hasText: 'Account' }).click()
  await expect(page).toHaveURL(/\/account$/)
  await expect(page.getByRole('heading', { name: 'Account', level: 1 })).toBeVisible()
  for (const name of ['Password', 'Multi-factor authentication', 'API tokens', 'Sessions']) {
    await expect(region(page, name)).toBeVisible()
  }
})

test('a viewer makes its own viewer token, which goes with the account', async ({
  page,
  browser,
  baseURL,
}) => {
  await page.setViewportSize(WIDE)
  await signIn(page, 'admin')
  const created = await page.request.post('/api/v1/users', {
    data: { username: 'quillon', password: PASSWORD, role: 'viewer' },
    headers,
  })
  expect(created.ok(), await created.text()).toBe(true)

  const context = await browser.newContext({ baseURL, viewport: WIDE })
  const viewer = await context.newPage()
  await signIn(viewer, 'quillon')
  await viewer.goto('/account')
  const tokens = region(viewer, 'API tokens')
  await expect(tokens).toContainText('No tokens.')
  await tokens.getByRole('button', { name: 'Add token' }).click()
  const dialog = viewer.getByRole('dialog')
  await expect(dialog.locator('#tok-role option')).toHaveText(['Viewer: read only'])
  await dialog.getByLabel('Name', { exact: true }).fill('lantern-feed')
  await dialog.getByRole('button', { name: 'Create token' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(tokens).toContainText('Copy lantern-feed now.')
  await tokens.getByRole('button', { name: 'Close' }).click()
  const row = tokens.getByRole('row').filter({ hasText: 'lantern-feed' })
  await expect(row).toContainText('viewer')
  await expect(row.getByRole('button', { name: 'Delete' })).toBeVisible()

  // The server holds the same line as the dialog.
  const above = await viewer.request.post('/api/v1/auth/tokens', {
    data: { name: 'too-high', role: 'admin' },
    headers,
  })
  expect(above.status()).toBe(400)

  await expect(region(viewer, 'Sessions').getByText('this browser')).toBeVisible()
  await viewer.screenshot({ path: `${SHOTS}/account-viewer.png`, fullPage: true })

  // System › Accounts is for admins: not in the sidebar, and not reachable.
  const menu = viewer.getByRole('navigation', { name: 'Main' })
  const system = menu.getByRole('button', { name: 'System', exact: true })
  if ((await system.getAttribute('aria-expanded')) !== 'true') await system.click()
  await expect(menu.getByRole('link', { name: 'General', exact: true })).toBeVisible()
  await expect(menu.getByRole('link', { name: 'Accounts', exact: true })).toHaveCount(0)
  await viewer.goto('/system/accounts')
  await expect(viewer).toHaveURL(/\/account$/)

  // The admin sees every account's tokens, and makes none there.
  await page.goto('/system/accounts')
  const all = region(page, 'API tokens')
  await expect(all).toContainText("Every account's tokens. New ones are made on the Account page.")
  await expect(all.getByRole('button', { name: 'Add token' })).toHaveCount(0)
  const owned = all.getByRole('row').filter({ hasText: 'lantern-feed' })
  await expect(owned.locator('td[data-label="Owner"]')).toHaveText('quillon')
  await page.screenshot({ path: `${SHOTS}/system-accounts-admin.png`, fullPage: true })

  const accounts = region(page, 'Accounts')
  await accounts
    .getByRole('row')
    .filter({ hasText: 'quillon' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await confirmDialog(page, { typed: 'quillon' })
  await expect(accounts.getByRole('row').filter({ hasText: 'quillon' })).toHaveCount(0)
  await page.reload()
  await expect(region(page, 'API tokens')).toContainText('No tokens.')
  await expect(page.getByText('lantern-feed')).toHaveCount(0)
  await context.close()
})

test('Sessions signs another browser out, one or all', async ({ page, browser, baseURL }) => {
  await signIn(page, 'admin')
  // The tests before this one left sessions of their own behind.
  const ended = await page.request.post('/api/v1/auth/sessions/end-others', { headers })
  expect(ended.ok(), await ended.text()).toBe(true)

  const context = await browser.newContext({ baseURL })
  const other = await context.newPage()
  const sessions = region(page, 'Sessions')
  const rows = sessions.locator('tbody tr')
  const others = sessions.getByRole('button', { name: 'Sign out the others' })

  await signIn(other, 'admin')
  await page.goto('/account')
  await expect(rows).toHaveCount(2)
  const current = rows.filter({ hasText: 'this browser' })
  await expect(current).toHaveCount(1)
  await expect(current).toContainText('Chrome on Windows')
  await expect(current.getByRole('button', { name: 'Sign out' })).toHaveCount(0)
  await rows
    .filter({ hasNotText: 'this browser' })
    .getByRole('button', { name: 'Sign out' })
    .click()
  await expect(rows).toHaveCount(1)
  await expect(rows).toContainText('this browser')
  await expect(others).toBeDisabled()
  await other.goto('/account')
  await expect(other).toHaveURL(/\/login/)

  await signIn(other, 'admin')
  await page.reload()
  await expect(rows).toHaveCount(2)
  await expect(others).toBeEnabled()
  await others.click()
  await confirmDialog(page, { confirm: 'Sign out' })
  await expect(rows).toHaveCount(1)
  await expect(rows).toContainText('this browser')
  await expect(others).toBeDisabled()
  await other.goto('/account')
  await expect(other).toHaveURL(/\/login/)
  await context.close()
})

test('the Password card changes the password', async ({ page }) => {
  const next = 'another long passphrase'
  const change = async (from: string, to: string) => {
    await page.goto('/account')
    const card = region(page, 'Password')
    await card.getByLabel('Current password').fill(from)
    await card.getByLabel('New password', { exact: true }).fill(to)
    await card.getByLabel('Repeat new password').fill(to)
    await card.getByRole('button', { name: 'Change password' }).click()
    await expect(card.getByRole('status')).toHaveText('Password changed.')
  }

  await signIn(page, 'admin')
  await change(PASSWORD, next)
  await signOut(page)
  await enterUsername(page, 'admin')
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('alert')).toContainText('Invalid username or password.')
  await signIn(page, 'admin', next)

  await change(next, PASSWORD)
  await signOut(page)
  await signIn(page, 'admin')
})
