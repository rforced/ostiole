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
