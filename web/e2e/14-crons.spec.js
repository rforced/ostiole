import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('schedule a nightly backup and see what the box does by itself', async ({ page }) => {
  await login(page)
  await page.goto('/crons')
  await expect(page.getByRole('heading', { name: 'Scheduled jobs' })).toBeVisible()

  // The work Ostiole does on its own account is listed whether or not
  // anyone has configured a job.
  const system = page.getByRole('region', { name: 'What Ostiole does by itself' })
  await expect(system).toContainText('Refresh the blocklists')
  await expect(system).toContainText('Probe each gateway')

  await page.getByRole('button', { name: 'Add job' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Nightly backup')
  await expect(dialog.getByLabel('Schedule')).toHaveValue('0 4 * * *')
  // The presets are there so nobody has to remember the field order.
  await dialog.getByLabel('When').selectOption('0 4 * * 0')
  await expect(dialog.getByLabel('Schedule')).toHaveValue('0 4 * * 0')
  await dialog.getByLabel('When').selectOption('0 4 * * *')
  await dialog.getByLabel('Write to').fill('/tmp/ostiole-e2e-backups')
  await dialog.getByLabel('Keep').fill('3')
  await page.screenshot({ path: shot('A0-cron-dialog'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const row = page.getByRole('row').filter({ hasText: 'Nightly backup' })
  await expect(row).toContainText('/tmp/ostiole-e2e-backups')
  await expect(row).toContainText('0 4 * * *')

  await applyAndConfirm(page)

  // Applied, so the daemon knows about it and works out the next run. It
  // has not run yet, so the last run is still "never".
  await page.reload()
  const applied = page.getByRole('row').filter({ hasText: 'Nightly backup' })
  await expect(applied).toContainText(/4:00:00/)
  await expect(applied).toContainText('never')
  await page.screenshot({ path: shot('A1-crons'), fullPage: true })

  // Running it now writes a real backup, and the result is reported.
  await applied.getByRole('button', { name: 'Run now' }).click()
  await expect(applied).toContainText('wrote /tmp/ostiole-e2e-backups/')
})

test('a bad schedule is refused by the server', async ({ page }) => {
  await login(page)
  await page.goto('/crons')
  await page.getByRole('button', { name: 'Add job' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Broken')
  await dialog.getByLabel('Schedule').fill('every tuesday please')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  await expect(page.getByRole('alert')).toContainText('five fields')
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})

test('a command job asks for an absolute path', async ({ page }) => {
  await login(page)
  await page.goto('/crons')
  await page.getByRole('button', { name: 'Add job' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('What it does').selectOption('command')
  await dialog.getByLabel('Command').fill('reboot')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(dialog.getByRole('alert')).toContainText('full path')

  await dialog.getByLabel('Command').fill('/usr/bin/systemctl')
  await dialog.getByLabel('Arguments').fill('restart\nostiole-dnsmasq')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(
    page.getByRole('row').filter({ hasText: '/usr/bin/systemctl restart ostiole-dnsmasq' }),
  ).toBeVisible()

  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})
