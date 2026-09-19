import { expect, test } from '@playwright/test'

import { login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('wireless says what a router without a card has', async ({ page }) => {
  await login(page)
  await page.goto('/wireless')
  await expect(page.getByRole('heading', { name: 'Wireless', exact: true })).toBeVisible()

  // The e2e server is not root and has no hostapd, so the note stands
  // rather than the page claiming a radio.
  await expect(page.getByRole('note')).toContainText('Not on this router')
  await expect(page.getByRole('cell', { name: 'No radios on this router.' })).toBeVisible()
  await page.screenshot({ path: shot('120-wireless'), fullPage: true })

  await page.getByRole('tab', { name: 'Networks' }).click()
  await expect(page.getByRole('cell', { name: 'No networks.' })).toBeVisible()
  await expect(page.getByText('Configure a radio first.')).toBeVisible()

  await page.getByRole('tab', { name: 'Clients' }).click()
  await expect(page.getByRole('cell', { name: 'Nothing to show.' })).toBeVisible()
})
