import { readFileSync } from 'node:fs'

import { expect, test } from '@playwright/test'

import { PASSWORD, applyAndConfirm, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('download a backup, change the router, then restore it', async ({ page }) => {
  await login(page)
  await page.goto('/system/backup')
  await expect(page.getByRole('heading', { name: 'Backup', exact: true, level: 1 })).toBeVisible()

  await page.getByLabel('Description').fill('before the hostname change')
  const download = page.waitForEvent('download')
  await page.getByRole('button', { name: 'Download backup' }).click()
  const file = await download
  expect(file.suggestedFilename()).toMatch(/^[\w.-]+-\d{8}-\d{6}\.json$/)

  const saved = JSON.parse(readFileSync(await file.path(), 'utf8'))
  expect(saved.kind).toBe('ostiole-backup')
  expect(saved.note).toBe('before the hostname change')
  expect(saved.config.rules.length).toBeGreaterThan(0)
  // Accounts stay out unless asked for.
  expect(saved.users ?? []).toHaveLength(0)
  const originalHostname = saved.config.system.hostname

  // Change something, apply it, then put the backup back.
  await sidebar(page, 'General')
  await page.getByLabel('Hostname').fill('renamed-for-the-test')
  await applyAndConfirm(page)

  await page.goto('/system/backup')
  await page.getByLabel('Backup file').setInputFiles(await file.path())
  const summary = page.getByRole('note').filter({ hasText: 'What it would change' })
  await expect(summary).toContainText('before the hostname change')
  await expect(summary).toContainText('system.hostname')
  await expect(summary).toContainText('renamed-for-the-test')
  await page.screenshot({ path: shot('80-restore'), fullPage: true })

  await summary.getByRole('button', { name: 'Load into draft' }).click()
  await sidebar(page, 'General')
  await expect(page.getByLabel('Hostname')).toHaveValue(originalHostname)
  await applyAndConfirm(page)
  await page.reload()
  await expect(page.getByLabel('Hostname')).toHaveValue(originalHostname)
})

test('a file that is not a backup is refused', async ({ page }) => {
  await login(page)
  await page.goto('/system/backup')
  await page.getByLabel('Backup file').setInputFiles({
    name: 'notes.json',
    mimeType: 'application/json',
    buffer: Buffer.from('{"hello":"world"}'),
  })
  await expect(page.getByRole('alert')).toContainText('not an Ostiole backup')
})

test('a revision can be compared with the running configuration', async ({ page }) => {
  await login(page)
  await page.goto('/system/backup')
  const history = page.getByRole('region', { name: 'Configuration history' })
  const row = history.getByRole('row').nth(1)

  await row.getByRole('button', { name: 'Compare with current' }).click()
  // The hostname went there and back, so something must have changed
  // between the newest archived revision and what is running now.
  await expect(history).toContainText('system.hostname')
  await page.screenshot({ path: shot('81-revision-diff'), fullPage: true })

  await row.getByRole('button', { name: 'Hide changes' }).click()
  await expect(history.getByText('system.hostname')).toHaveCount(0)
})

// The bucket itself is the Go tests' business; what matters here is that
// the settings save and become the cron that takes the copies. The
// endpoint is a name that cannot resolve, so nothing leaves the machine.
test('the remote backup settings become a system cron', async ({ page }) => {
  await login(page)
  await page.goto('/system/backup')
  const section = page.getByRole('region', { name: 'Remote backup' })
  await expect(section).toBeVisible()

  await section.getByLabel('Endpoint', { exact: true }).fill('https://s3.invalid')
  await section.getByLabel('Bucket', { exact: true }).fill('router-backups')
  await section.getByLabel('Key ID', { exact: true }).fill('0055abc')
  await section.getByLabel('Secret', { exact: true }).fill('not-a-real-key')
  await section.getByLabel('Passphrase', { exact: true }).fill(PASSWORD)
  // Schedule, prefix and retention sit behind the fold.
  await section.getByRole('button', { name: 'Advanced' }).click()
  await expect(section.getByLabel('Schedule', { exact: true })).toHaveValue('0 3 * * *')
  await section.getByLabel('Remote backup enabled').check()
  await page.screenshot({ path: shot('82-remote-backup'), fullPage: true })
  await applyAndConfirm(page)

  await sidebar(page, 'Crons')
  const system = page.getByRole('region', { name: "Ostiole's crons" })
  const row = system.getByRole('row').filter({ hasText: 'Remote backup' })
  await expect(row).toContainText('0 3 * * *')

  // Off again: the specs that follow are not about a bucket.
  await sidebar(page, 'System', 'Backup')
  await page
    .getByRole('region', { name: 'Remote backup' })
    .getByLabel('Remote backup enabled')
    .uncheck()
  await applyAndConfirm(page)
})
