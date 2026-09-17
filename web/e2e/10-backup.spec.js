import { readFileSync } from 'node:fs'

import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('download a backup, change the box, then restore it', async ({ page }) => {
  await login(page)
  await page.goto('/system/backup')
  await expect(page.getByRole('heading', { name: 'Backup and restore' })).toBeVisible()

  await page.getByLabel('Note').fill('before the hostname change')
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
