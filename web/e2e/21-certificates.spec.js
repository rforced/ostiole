import { expect, test } from '@playwright/test'

import { applyAndConfirm, confirmDialog, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('the Web UI card says there is nothing to serve without HTTPS', async ({ page }) => {
  await login(page)
  await page.goto('/system/certificates')
  const card = page.getByRole('region', { name: 'Web UI' })
  await expect(card).toContainText('not serving HTTPS')
  await expect(card.getByRole('button', { name: 'Regenerate self-signed' })).toHaveCount(0)
})

test('order a certificate over dns-01', async ({ page }) => {
  await login(page)
  await page.goto('/system/certificates#providers')

  await page.getByRole('button', { name: 'Add provider' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('local')
  await dialog.getByLabel('Kind').selectOption('exec')
  await dialog.getByLabel('Program').fill('/bin/true')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'local' })).toContainText('Program')

  await page.getByRole('tab', { name: 'ACME accounts' }).click()
  await page.getByRole('button', { name: 'Add account' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('staging')
  await dialog.getByLabel('CA', { exact: true }).selectOption({ label: "Let's Encrypt (staging)" })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'staging' })).toContainText(
    'acme-staging-v02',
  )

  await page.getByRole('tab', { name: 'Certificates', exact: true }).click()
  await page.getByRole('button', { name: 'Add certificate' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('router')
  await dialog.getByLabel('Names').fill('router.example.test')
  await expect(dialog.getByLabel('Challenge')).toHaveValue('dns-01')
  await expect(dialog.getByLabel('Account')).toHaveValue('staging')
  await expect(dialog.getByLabel('DNS provider')).toHaveValue('local')
  await page.screenshot({ path: shot('60-certificate-dialog'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  await applyAndConfirm(page)

  // Nothing has been ordered yet: the cron does that, hourly.
  const row = page.getByRole('row').filter({ hasText: 'router.example.test' })
  await expect(row).toContainText('not issued')
  await page.screenshot({ path: shot('61-certificates'), fullPage: true })

  const cfg = await (await page.request.get('/api/v1/config')).json()
  expect(cfg.certificates[0]).toMatchObject({
    id: 'router',
    source: 'acme',
    challenge: 'dns-01',
    account: 'staging',
    provider: 'local',
  })

  // The renewal cron is derived from the certificate, not written out: the
  // row is always listed, and gains an hourly schedule once there is work.
  await page.goto('/system/crons')
  await expect(
    page
      .getByRole('region', { name: "Ostiole's crons" })
      .getByRole('row')
      .filter({ hasText: 'Renew and issue certificates' }),
  ).toContainText(/\d+ \* \* \* \*/)
})

test('a token limited to a certificate reaches that and nothing else', async ({
  page,
  request,
}) => {
  await login(page)
  await page.goto('/system/accounts')
  const section = page.getByRole('region', { name: 'API tokens' })

  await section.getByRole('button', { name: 'Add token' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('cert-fetcher')
  await dialog.getByLabel('Role', { exact: true }).selectOption('admin')
  await dialog.getByLabel('router').check()
  await dialog.getByRole('button', { name: 'Create token' }).click()

  const shown = section.getByRole('note')
  const secret = (await shown.locator('code').innerText()).trim()
  await shown.getByRole('button', { name: 'Close' }).click()
  await expect(section.getByRole('row').filter({ hasText: 'cert-fetcher' })).toContainText(
    'certificates: router',
  )

  const headers = { Authorization: `Bearer ${secret}` }
  // Nothing is issued, so the files are missing rather than refused.
  const files = await request.get('/api/v1/certificates/router/files', { headers })
  expect(files.status()).toBe(404)
  // And the rest of the API is closed to it, admin role or not.
  const config = await request.get('/api/v1/config', { headers })
  expect(config.status()).toBe(403)

  await section
    .getByRole('row')
    .filter({ hasText: 'cert-fetcher' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await confirmDialog(page, { typed: 'cert-fetcher' })
})

test('delete the certificate, its account and its provider', async ({ page }) => {
  await login(page)
  await page.goto('/system/certificates')

  await page
    .getByRole('row')
    .filter({ hasText: 'router.example.test' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await confirmDialog(page, { typed: 'router' })

  await page.getByRole('tab', { name: 'ACME accounts' }).click()
  await page
    .getByRole('row')
    .filter({ hasText: 'staging' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await confirmDialog(page, { typed: 'staging' })

  await page.getByRole('tab', { name: 'DNS providers' }).click()
  await page
    .getByRole('row')
    .filter({ hasText: 'local' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await confirmDialog(page, { typed: 'local' })

  await applyAndConfirm(page)
  const cfg = await (await page.request.get('/api/v1/config')).json()
  expect(cfg.certificates ?? []).toHaveLength(0)
  expect(cfg.acme?.accounts ?? []).toHaveLength(0)
})
