import { expect, test } from '@playwright/test'

import { applyAndConfirm, confirmDialog, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('schedule a nightly backup and see what the router does by itself', async ({ page }) => {
  await login(page)
  await page.goto('/system/crons')
  // Exact: the page also has a "Your cron jobs" heading, which a substring
  // match resolves to as well.
  await expect(
    page.getByRole('heading', { name: 'Cron jobs', exact: true, level: 1 }),
  ).toBeVisible()

  // The work Ostiole does on its own account is listed whether or not
  // anyone has configured a cron, and every timer the daemon starts is
  // on it.
  const system = page.getByRole('region', { name: "Ostiole's cron jobs" })
  await expect(system).toContainText('Refresh firewall alias lists')
  await expect(system).toContainText('Refresh DNS block lists')
  await expect(system).toContainText('Probe each gateway')
  await expect(system).toContainText('Expire idle web sessions')
  await expect(system).toContainText('Firewall packet collector')
  await expect(system).toContainText('Renew and issue certificates')

  await page.getByRole('button', { name: 'Add cron job' }).click()
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
  await confirmDialog(page, { confirm: 'Run' })
  await expect(applied).toContainText('wrote /tmp/ostiole-e2e-backups/')
})

test('a bad schedule is refused by the server', async ({ page }) => {
  await login(page)
  await page.goto('/system/crons')
  await page.getByRole('button', { name: 'Add cron job' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Broken')
  await dialog.getByLabel('Schedule').fill('every tuesday please')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  await expect(page.getByRole('alert')).toContainText('five fields')
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})

test('a command cron asks for an absolute path', async ({ page }) => {
  await login(page)
  await page.goto('/system/crons')
  await page.getByRole('button', { name: 'Add cron job' }).click()
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
