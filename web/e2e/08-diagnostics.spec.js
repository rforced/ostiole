import { expect, test } from '@playwright/test'

import { login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('the ping form returns a result or an error, and never hangs', async ({ page }) => {
  await login(page)
  await page.goto('/diagnostics')
  await expect(page).toHaveURL(/\/diagnostics\/ping$/)
  await expect(page.getByRole('heading', { name: 'Ping and traceroute', level: 1 })).toBeVisible()

  // The loopback always answers, so this works even in a test sandbox.
  await page.getByLabel('Target').fill('127.0.0.1')
  await page.getByLabel('Probes').fill('2')
  await page.getByRole('button', { name: 'Ping', exact: true }).click()

  // The result card is named by what was pinged, the way every card is.
  const result = page.getByRole('region', { name: '127.0.0.1' })
  // Without root the daemon cannot open a raw socket; either answer is a
  // working page, so accept the error too.
  await expect(async () => {
    const ok = await result.isVisible()
    const failed = await page.getByRole('alert').isVisible()
    expect(ok || failed).toBe(true)
  }).toPass()
  if (await result.isVisible()) {
    await expect(result).toContainText('127.0.0.1')
    await expect(result).toContainText('answered')
  }
  await page.screenshot({ path: shot('70-diagnostics'), fullPage: true })
})

test('the log viewer answers a query without hanging or going blank', async ({ page }) => {
  await login(page)
  await page.goto('/diagnostics/logs')
  await expect(page.getByLabel('Unit')).toBeVisible()
  await page.getByLabel('Since').fill('-5min')
  await page.getByRole('button', { name: /Refresh|Reading/ }).click()
  // On a router without systemd the call fails cleanly rather than hanging.
  await expect(async () => {
    const shown = await page.getByText('Nothing in this window.').isVisible()
    const failed = await page.getByRole('alert').isVisible()
    const lines = await page.locator('.font-mono.text-code p').count()
    expect(shown || failed || lines > 0).toBe(true)
  }).toPass()
})

test('the capture page picks an interface, so the button is live', async ({ page }) => {
  await login(page)
  await page.goto('/diagnostics/capture')
  await expect(page.getByLabel('Interface')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Capture' })).toBeEnabled()
})

test('the connections and neighbour pages explain themselves, and the filter empties the list', async ({
  page,
}) => {
  await login(page)
  await page.goto('/diagnostics/connections')
  // The dev host tracks connections; a machine without the module says so
  // rather than showing an empty table with no explanation.
  const states = page.getByRole('main')
  await expect(states).toContainText(
    /connection\(s\) tracked|connection table|tracking no connections/,
  )
  await page.getByLabel('Protocol').selectOption('tcp')
  await page.getByRole('button', { name: 'Refresh' }).click()
  // Either a table or an explanation, never a silently empty panel.
  await expect(states).toContainText(
    /connection\(s\) tracked|connection table|tracking no connections/,
  )
  await page.screenshot({ path: shot('45-states'), fullPage: true })

  await sidebar(page, 'ARP and NDP')
  const neigh = page.getByRole('main')
  await expect(neigh).toContainText(/ of \d+/)
  await page.getByPlaceholder('address, MAC, or interface').fill('zzz-nothing')
  await expect(neigh).toContainText('No neighbours.')
  await page.screenshot({ path: shot('46-neighbours'), fullPage: true })
})

test('the drives page reads the drive and offers a self-test', async ({ page }) => {
  await login(page)
  await page.goto('/diagnostics/drives')
  const main = page.getByRole('main')
  await expect(main).toContainText('GOFATOO 256GB SSD')
  await expect(main.getByText('passed', { exact: true })).toBeVisible()
  // The whole attribute table, so a drive that is losing sectors shows it.
  // It is folded away until asked for, like the two logs under it.
  await page.getByRole('button', { name: /^Attributes/ }).click()
  await expect(page.locator('tbody tr').filter({ hasText: 'Reallocated_Sector_Ct' })).toHaveCount(1)
  await expect(main.locator('table').first().locator('tbody tr')).toHaveCount(30)
  await expect(page.getByRole('button', { name: 'Short test' })).toBeEnabled()
  await page.screenshot({ path: shot('47-drives'), fullPage: true })
})
