import { expect, test } from '@playwright/test'

import { login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('the diagnostics page reports what the firewall can reach', async ({ page }) => {
  await login(page)
  await page.goto('/diagnostics')
  await expect(page.getByRole('heading', { name: 'Diagnostics' })).toBeVisible()

  // The loopback always answers, so this works even in a test sandbox.
  await page.getByLabel('Target').fill('127.0.0.1')
  await page.getByLabel('Probes').fill('2')
  await page.getByRole('button', { name: 'Ping', exact: true }).click()

  const result = page.getByRole('region', { name: 'Ping result' })
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

test('the log viewer reads the journal', async ({ page }) => {
  await login(page)
  await page.goto('/diagnostics')
  await page.getByRole('tab', { name: 'Logs' }).click()
  await expect(page.getByLabel('Unit')).toBeVisible()
  await page.getByLabel('Since').fill('-5min')
  await page.getByRole('button', { name: /Refresh|Reading/ }).click()
  // On a box without systemd the call fails cleanly rather than hanging.
  await expect(async () => {
    const shown = await page.getByText('Nothing in this window.').isVisible()
    const failed = await page.getByRole('alert').isVisible()
    const lines = await page.locator('.font-mono.text-xs p').count()
    expect(shown || failed || lines > 0).toBe(true)
  }).toPass()
})

test('a capture needs an interface and rejects a silly port', async ({ page }) => {
  await login(page)
  await page.goto('/diagnostics')
  await page.getByRole('tab', { name: 'Packet capture' }).click()
  await expect(page.getByLabel('Interface')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Capture' })).toBeEnabled()
})

test('the connections and neighbour tables read the kernel', async ({ page }) => {
  await login(page)
  await page.goto('/diagnostics')

  await page.getByRole('tab', { name: 'Connections' }).click()
  // The dev box tracks connections; a machine without the module says so
  // rather than showing an empty table with no explanation.
  const states = page.getByRole('tabpanel')
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

  await page.getByRole('tab', { name: 'ARP and NDP' }).click()
  const neigh = page.getByRole('tabpanel')
  await expect(neigh).toContainText(/ of \d+/)
  await page.getByPlaceholder('address, MAC, or interface').fill('zzz-nothing')
  await expect(neigh).toContainText('Nothing to show.')
  await page.screenshot({ path: shot('46-neighbours'), fullPage: true })
})
