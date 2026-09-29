import { expect, test } from './fixtures.js'
import { applyAndConfirm, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// The e2e server is not root: it reads the links' counters, but not the
// connection table, and says so once counting per device is on.
test('the links chart from the stream, and counting per device goes on', async ({ page }) => {
  await login(page)
  await sidebar(page, 'Traffic')
  await expect(page.getByRole('heading', { name: 'Traffic', level: 1 })).toBeVisible()
  await expect(page.getByRole('tab', { name: 'Interfaces' })).toHaveAttribute(
    'aria-selected',
    'true',
  )
  await expect(page.getByLabel('Window')).toHaveValue('5m')
  const line = page
    .getByRole('img', { name: /: down / })
    .first()
    .locator('path')
    .first()
  await expect(line).toHaveAttribute('d', /L/)
  const points = async () => ((await line.getAttribute('d')) ?? '').split('L').length
  const before = await points()
  // A point a second, from the stream.
  await expect.poll(points, { timeout: 10_000 }).toBeGreaterThan(before)
  await page.screenshot({ path: shot('B0-traffic-interfaces'), fullPage: true })

  // A day reads minutes, with nothing moving before the counts began.
  await page.getByLabel('Window').selectOption('24h')
  await expect(page.getByText(/^Since /).first()).toBeVisible()
  await page.getByRole('button', { name: 'Table' }).first().click()
  await expect(page.getByRole('table').first().locator('tbody tr').first()).toBeVisible()

  await page.getByRole('tab', { name: 'Devices' }).click()
  const counting = page.getByLabel('Count traffic per device')
  await expect(counting).not.toBeChecked()
  await counting.check()
  await expect(page.getByText('Apply the draft to start it.')).toBeVisible()
  await applyAndConfirm(page)
  await expect(page.getByRole('alert')).toContainText('Could not read the connection table', {
    timeout: 15_000,
  })
  await expect(page.getByRole('region', { name: 'Devices' })).toContainText('No devices.')
  await page.screenshot({ path: shot('B1-traffic-devices'), fullPage: true })
})

// Destinations need the devices counted, which the test above switched on.
// The server reads no connections, so it has none to name.
test('destinations go on beside the devices', async ({ page }) => {
  await login(page)
  await page.goto('/traffic#destinations')
  const record = page.getByRole('switch', { name: 'Record destinations' })
  await expect(record).toBeEnabled()
  await record.check()
  await expect(page.getByLabel('Days')).toHaveAttribute('placeholder', '7')
  await applyAndConfirm(page)
  await expect(page.getByRole('region', { name: 'Destinations' })).toContainText('No destinations.')
  await page.screenshot({ path: shot('B2-traffic-destinations'), fullPage: true })
})
