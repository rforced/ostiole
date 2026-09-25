import { expect, test } from '@playwright/test'

import { applyAndConfirm, confirmDialog, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('the page is honest about the sidecar not being installed', async ({ page }) => {
  await login(page)
  await page.goto('/services/proxy')
  await expect(
    page.getByRole('heading', { name: 'Reverse proxy', exact: true, level: 1 }),
  ).toBeVisible()
  // The e2e server is not root and has no ostiole-proxy, so the strip says
  // so rather than the page claiming the proxy runs.
  await expect(page.getByRole('note')).toContainText('Not on this router')
  await expect(page.getByRole('note')).toContainText('ostiole repair --proxy')
})

test('publish a site through a pool and apply it', async ({ page }) => {
  await login(page)
  await page.goto('/services/proxy')

  // Ports of its own, so nothing else in the suite has to move.
  await page.getByLabel('Reverse proxy enabled').check()
  // Nothing reaches the proxy until a zone is ticked, and validation says so.
  await page.getByRole('checkbox', { name: 'wan', exact: true }).check()
  await page.getByRole('spinbutton', { name: 'HTTP port' }).fill('8080')
  await page.getByRole('spinbutton', { name: 'HTTPS port' }).fill('8443')

  await page.getByRole('tab', { name: 'Pools' }).click()
  await page.getByRole('button', { name: 'Add pool' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('web')
  await dialog.getByRole('textbox', { name: 'Upstreams' }).fill('192.168.50.20:8080')
  await dialog.getByLabel('Health path').fill('/healthz')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'web' })).toContainText('192.168.50.20:8080')

  await page.getByRole('tab', { name: 'WAF' }).click()
  await page.getByRole('button', { name: 'Add profile' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('watch')
  await dialog.getByLabel('wordpress').check()
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const profile = page.getByRole('row').filter({ hasText: 'watch' })
  await expect(profile).toContainText('detect only')
  await expect(profile).toContainText('wordpress')

  await page.getByRole('tab', { name: 'Sites' }).click()
  await page.getByRole('button', { name: 'Add site' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('shop')
  await dialog.getByLabel('Hostnames').fill('shop.example.com')
  await dialog.getByLabel('WAF profile').selectOption('watch')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const site = page.getByRole('row').filter({ hasText: 'shop' })
  await expect(site).toContainText('shop.example.com')
  await expect(site).toContainText('built-in')

  await page.getByRole('tab', { name: 'Routes' }).click()
  await page.getByRole('button', { name: 'Add route' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('imap')
  await dialog.getByRole('spinbutton', { name: 'Port' }).fill('993')
  await dialog.getByRole('textbox', { name: 'Upstreams' }).fill('192.168.50.30:993')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'imap' })).toContainText('tcp/993')
  await page.screenshot({ path: shot('110-proxy-routes'), fullPage: true })

  await applyAndConfirm(page)

  // The listeners are opened at the tail of the external zone's chain.
  const ruleset = await page.evaluate(async () => {
    const res = await fetch('/api/v1/ruleset', { headers: { 'X-Requested-With': 'ostiole' } })
    return await res.text()
  })
  expect(ruleset).toContain('service:proxy')
  expect(ruleset).toContain('tcp dport { 993, 8080, 8443 }')
})

test('a reload comes back to the tab it was on', async ({ page }) => {
  await login(page)
  await page.goto('/services/proxy#sites')
  await expect(page.getByRole('tab', { name: 'Sites' })).toHaveAttribute('aria-selected', 'true')
  await page.reload()
  await expect(page).toHaveURL(/#sites$/)
  await expect(page.getByRole('row').filter({ hasText: 'shop' })).toBeVisible()
})

test('deleting a site asks for its name back', async ({ page }) => {
  await login(page)
  await page.goto('/services/proxy#sites')
  await page
    .getByRole('row')
    .filter({ hasText: 'shop' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await confirmDialog(page, { typed: 'shop' })
  await expect(page.getByText('No sites.')).toBeVisible()
  await applyAndConfirm(page)
})
