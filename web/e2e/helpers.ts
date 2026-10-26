import { expect, type APIRequestContext, type Page } from '@playwright/test'

import type { Config } from '../src/lib/model.gen'

export const PASSWORD = 'correct horse battery'
export const shot = (name: string) => `e2e/screenshots/${name}.png`

/** Signs in as the admin global-setup.ts created, as 01-first-run.spec.ts does. */
export async function login(page: Page) {
  await page.goto('/login')
  await enterUsername(page, 'admin')
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/$/)
}

/** The first step of the sign-in page: the password is asked after it. */
export async function enterUsername(page: Page, username: string) {
  await page.getByLabel('Username').fill(username)
  await page.getByRole('button', { name: 'Continue' }).click()
}

/** Signs out from the account menu at the foot of the sidebar. */
export async function signOut(page: Page) {
  await page.getByRole('button', { name: 'Account menu' }).click()
  await page.getByRole('menuitem', { name: 'Sign out' }).click()
  await expect(page.getByLabel('Username')).toBeVisible()
}

/**
 * Opens pages from the sidebar, in order: `sidebar(page, 'Firewall', 'Aliases')`
 * from anywhere, or just `sidebar(page, 'Aliases')` when the section is already
 * open. Navigating in-app keeps the draft that a `page.goto()` would discard.
 * The labels are the sidebar's, outermost first.
 */
export async function sidebar(page: Page, ...pages: string[]) {
  const menu = page.getByRole('navigation', { name: 'Main' })
  for (const name of pages) {
    // A section is a button that opens its pages, and a second click
    // would close them again.
    const row = menu
      .getByRole('button', { name, exact: true })
      .or(menu.getByRole('link', { name, exact: true }))
    if ((await row.getAttribute('aria-expanded')) !== 'true') await row.click()
  }
}

/** Applies the draft from the apply bar and confirms it. */
export async function applyAndConfirm(page: Page) {
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  const pending = page.getByRole('status')
  await expect(pending).toContainText('awaiting confirmation')
  await pending.getByRole('button', { name: 'Confirm' }).click()
  await expect(pending).toContainText('Confirmed and saved')
  await expect(page.getByRole('status')).toHaveCount(0, { timeout: 10_000 })
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
}

/**
 * The saved configuration and its revision, which an apply sends back as
 * baseRevision. The API context is signed in.
 */
export async function readConfig(api: APIRequestContext) {
  const res = await api.get('/api/v1/config')
  expect(res.ok(), await res.text()).toBe(true)
  const baseRevision = (res.headers().etag ?? '').replace(/^(W\/)?"|"$/g, '')
  return { config: await res.json(), baseRevision }
}

/**
 * Changes the running configuration through the API, applied and
 * confirmed, for what a file needs in place before its pages are opened.
 * The API context is signed in: page.request after login(), or a context
 * of its own. The change edits the configuration in place.
 */
export async function reconfigure(api: APIRequestContext, change: (config: Config) => void) {
  const headers = { 'X-Requested-With': 'ostiole' }
  const { config, baseRevision } = await readConfig(api)
  change(config)
  const applied = await api.post('/api/v1/apply', {
    data: { config, confirmTimeoutSeconds: 60, baseRevision },
    headers,
  })
  expect(applied.ok(), await applied.text()).toBe(true)
  const confirmed = await api.post('/api/v1/apply/confirm', { headers })
  expect(confirmed.ok(), await confirmed.text()).toBe(true)
}

/**
 * Answers the shared confirmation dialog that every delete and other
 * destructive action opens. Types the name back when the dialog asks for
 * one, then presses the button that says yes.
 */
export async function confirmDialog(
  page: Page,
  { typed = '', confirm = 'Delete' }: { typed?: string; confirm?: string } = {},
) {
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  if (typed) await dialog.getByLabel(`Type ${typed} to confirm`).fill(typed)
  await dialog.getByRole('button', { name: confirm, exact: true }).click()
  await expect(dialog).toHaveCount(0)
}
