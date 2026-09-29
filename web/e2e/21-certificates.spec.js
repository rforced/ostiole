import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { expect, test } from './fixtures.js'
import { applyAndConfirm, confirmDialog, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

/** A self-signed pair made for the test, so no key is ever committed. */
function selfSigned(name) {
  const dir = mkdtempSync(join(tmpdir(), 'ostiole-e2e-'))
  try {
    // Piped, so its progress stays out of the report and in the error.
    execFileSync(
      'openssl',
      [
        'req',
        '-x509',
        '-newkey',
        'ec',
        '-pkeyopt',
        'ec_paramgen_curve:P-256',
        '-nodes',
        '-days',
        '365',
        '-subj',
        `/CN=${name}`,
        '-addext',
        `subjectAltName=DNS:${name}`,
        '-keyout',
        join(dir, 'key.pem'),
        '-out',
        join(dir, 'cert.pem'),
      ],
      { stdio: 'pipe' },
    )
    return {
      cert: readFileSync(join(dir, 'cert.pem'), 'utf8'),
      key: readFileSync(join(dir, 'key.pem'), 'utf8'),
    }
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

test('the Web UI card says there is nothing to serve without HTTPS', async ({ page }) => {
  await login(page)
  await page.goto('/system/certificates')
  const card = page.getByRole('region', { name: 'Web UI' })
  await expect(card).toContainText('not serving HTTPS')
  await expect(card.getByRole('button', { name: 'Regenerate self-signed' })).toHaveCount(0)
})

test('order a certificate over dns-01', async ({ page }) => {
  await login(page)
  await page.goto('/system/dns-providers')

  await page.getByRole('button', { name: 'Add provider' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('local')
  await dialog.getByLabel('Kind').selectOption('exec')
  await dialog.getByLabel('Domains').fill('example.test')
  await dialog.getByLabel('Program').fill('/bin/true')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'local' })).toContainText('Program')
  await expect(page.getByRole('row').filter({ hasText: 'local' })).toContainText('example.test')

  // In the app, not page.goto: a full load would throw the draft away.
  await sidebar(page, 'System', 'Certificates')
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
  // The provider holding example.test writes the challenge unless another is named.
  await expect(dialog.getByLabel('DNS provider')).toHaveValue('')
  await expect(dialog.getByLabel('DNS provider').locator('option').first()).toHaveText(
    'Automatic (local)',
  )
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
  })
  expect(cfg.certificates[0].provider).toBeUndefined()
  expect(cfg.dnsProviders).toEqual([
    { id: 'local', kind: 'exec', settings: { program: '/bin/true' }, domains: ['example.test'] },
  ])

  // The renewal cron is derived from the certificate, not written out: the
  // row is always listed, and gains an hourly schedule once there is work.
  await page.goto('/system/crons')
  await expect(
    page
      .getByRole('region', { name: "Ostiole's cron jobs" })
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

  await sidebar(page, 'System', 'DNS providers')
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
  expect(cfg.dnsProviders ?? []).toHaveLength(0)
})

test('download an uploaded certificate from its Download menu', async ({ page }) => {
  const { cert, key } = selfSigned('files.example.test')
  await login(page)
  await page.goto('/system/certificates')

  await page.getByRole('button', { name: 'Add certificate' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('files')
  await dialog.getByLabel('Source').selectOption('uploaded')
  await dialog.getByLabel('Certificate (PEM)').fill(cert)
  await dialog.getByLabel('Private key (PEM)').fill(key)
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await applyAndConfirm(page)

  // The files are written within a few seconds of the apply.
  const row = page.getByRole('row').filter({ hasText: 'files' })
  const menu = row.getByRole('button', { name: 'Download' })
  const refresh = page
    .getByRole('region', { name: 'Web UI' })
    .getByRole('button', { name: 'Refresh' })
  await expect(async () => {
    await refresh.click()
    await expect(menu).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 15_000 })

  await menu.click()
  await expect(page.getByRole('menuitem')).toHaveText([
    'Full chain',
    'Certificate',
    'Chain',
    'Private key',
    'PKCS#12',
  ])
  await page.screenshot({ path: shot('62-certificate-downloads'), fullPage: true })
  const [chain] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('menuitem', { name: 'Full chain' }).click(),
  ])
  expect(chain.suggestedFilename()).toBe('files-fullchain.pem')
  expect(readFileSync(await chain.path(), 'utf8').trim()).toBe(cert.trim())
  await expect(page.getByRole('menu')).toHaveCount(0)
  await expect(menu).toBeFocused()

  // PKCS#12 asks for a password first, and its dialog hands focus back to
  // the menu's button rather than to an item that is gone.
  await menu.click()
  await page.getByRole('menuitem', { name: 'PKCS#12' }).click()
  await dialog.getByLabel('Password').fill('secret')
  const [bundle] = await Promise.all([
    page.waitForEvent('download'),
    dialog.getByRole('button', { name: 'Download' }).click(),
  ])
  expect(bundle.suggestedFilename()).toBe('files.p12')
  await expect(dialog).toHaveCount(0)
  await expect(menu).toBeFocused()

  await row.getByRole('button', { name: 'Delete' }).click()
  await confirmDialog(page, { typed: 'files' })
  await applyAndConfirm(page)
})
