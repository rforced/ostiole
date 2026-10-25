import { expect, test } from './fixtures.js'
import { login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('the dashboard summarises interfaces, rules, services, and warnings', async ({ page }) => {
  await login(page)
  await expect(page.getByRole('heading', { name: 'Dashboard', level: 1 })).toBeVisible()

  // The LAN the wizard configured, with the address it should carry.
  const interfaces = page.getByRole('region', { name: 'Interfaces' })
  await expect(interfaces).toContainText('192.168.50.1/24')
  await expect(interfaces).toContainText('lan')
  await expect(interfaces).toContainText('wan')

  // Counters come from the kernel (the stub nft here).
  const rules = page.getByRole('region', { name: 'Busiest rules' })
  await expect(rules).toContainText('Allow LAN to any')
  await expect(rules).toContainText('42')
  await expect(rules).toContainText('Blocked by the default policy: 9 packets')

  // DHCP and DNS as the services test left them.
  const services = page.getByRole('region', { name: 'Services' })
  await expect(services).toContainText('of 100')
  await expect(services).toContainText('lan')

  // The stub reports a table Ostiole does not own.
  const foreign = page.locator('[data-warning="foreign-tables"]')
  await expect(foreign).toContainText('ip nat')

  await page.screenshot({ path: shot('50-dashboard'), fullPage: true })
})

test('the dashboard keeps the layout this browser gives it', async ({ page }) => {
  await login(page)
  await expect(page.locator('section[aria-busy="true"]')).toHaveCount(0)
  const cards = () =>
    page.locator('[data-card]').evaluateAll((els) => els.map((el) => el.dataset.card))
  const before = async (a, b) => {
    const order = await cards()
    return order.indexOf(a) < order.indexOf(b)
  }
  expect(await before('rules', 'router')).toBe(true)

  await page.getByRole('button', { name: 'Layout' }).click()
  const dialog = page.getByRole('dialog', { name: 'Dashboard layout' })
  await expect(dialog.getByRole('button', { name: 'Restore' })).toHaveCount(0)

  await dialog.getByRole('switch', { name: 'Show Services' }).uncheck()
  await expect(page.locator('[data-card="services"]')).toHaveCount(0)

  // Router onto the slot Busiest rules holds, with the mouse.
  const handle = await dialog
    .locator('li', { hasText: 'Router' })
    .locator('[data-handle]')
    .boundingBox()
  const target = await dialog.locator('li', { hasText: 'Busiest rules' }).boundingBox()
  await page.mouse.move(handle.x + handle.width / 2, handle.y + handle.height / 2)
  await page.mouse.down()
  await page.mouse.move(handle.x + handle.width / 2, target.y + target.height / 2, { steps: 12 })
  await page.mouse.up()
  await expect.poll(() => before('router', 'rules')).toBe(true)
  await dialog
    .locator('ul')
    .evaluate((ul) => Promise.all(ul.getAnimations({ subtree: true }).map((a) => a.finished)))
  await page.screenshot({ path: shot('50-dashboard-layout') })

  await dialog.getByRole('button', { name: 'Close' }).last().click()
  await page.reload()
  await expect(page.locator('section[aria-busy="true"]')).toHaveCount(0)
  expect(await before('router', 'rules')).toBe(true)
  await expect(page.locator('[data-card="services"]')).toHaveCount(0)

  await page.getByRole('button', { name: 'Layout' }).click()
  await dialog.getByRole('button', { name: 'Restore' }).click()
  await expect(dialog.getByRole('button', { name: 'Restore' })).toHaveCount(0)
  await expect(page.locator('[data-card="services"]')).toHaveCount(1)
  expect(await before('rules', 'router')).toBe(true)
})

test('the dashboard refreshes on demand', async ({ page }) => {
  await login(page)
  const overview = page.waitForResponse((r) => r.url().includes('/api/v1/overview'))
  await page.getByRole('button', { name: 'Refresh' }).click()
  expect((await overview).status()).toBe(200)
})
