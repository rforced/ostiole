import { expect } from '@playwright/test'

export const PASSWORD = 'correct horse battery'
export const shot = (name) => `e2e/screenshots/${name}.png`

/** Signs in as the admin created by 01-first-run.spec.js. */
export async function login(page) {
  await page.goto('/login')
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/$/)
}

/**
 * Opens pages from the sidebar, in order: `sidebar(page, 'Firewall', 'Aliases')`
 * from anywhere, or just `sidebar(page, 'Aliases')` when the section is already
 * open. Navigating in-app keeps the draft that a `page.goto()` would discard.
 *
 * @param {import('@playwright/test').Page} page
 * @param {...string} pages sidebar labels to click, outermost first
 */
export async function sidebar(page, ...pages) {
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
export async function applyAndConfirm(page) {
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  const pending = page.getByRole('status')
  await expect(pending).toContainText('awaiting confirmation')
  await pending.getByRole('button', { name: 'Confirm' }).click()
  await expect(pending).toContainText('Confirmed and saved')
  await expect(page.getByRole('status')).toHaveCount(0, { timeout: 10_000 })
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
}

/**
 * Answers the shared confirmation dialog that every delete and other
 * destructive action opens. Types the name back when the dialog asks for
 * one, then presses the button that says yes.
 *
 * @param {import('@playwright/test').Page} page
 * @param {{typed?: string, confirm?: string}} [opts]
 */
export async function confirmDialog(page, { typed = '', confirm = 'Delete' } = {}) {
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  if (typed) await dialog.getByLabel(`Type ${typed} to confirm`).fill(typed)
  await dialog.getByRole('button', { name: confirm, exact: true }).click()
  await expect(dialog).toHaveCount(0)
}
