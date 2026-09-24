import { expect, test } from '@playwright/test'

import { login } from './helpers.js'

// The phone layout, run last by the mobile project against the router the
// rest of the suite configured.
test.describe.configure({ mode: 'serial' })

const shot = (name) => `e2e/screenshots/mobile/${name}.png`

/** How far the page scrolls sideways, which on a phone it never should. */
const overflow = (page) =>
  page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)

/** Some pages stream and never go quiet; they get the cap. */
const settle = (page) => page.waitForLoadState('networkidle', { timeout: 2000 }).catch(() => {})

/** Waits out whatever is sliding in; a spinner or a skeleton never ends. */
const still = (page) =>
  page.evaluate(() =>
    Promise.all(
      document
        .getAnimations()
        .filter((a) => a.effect?.getTiming().iterations !== Infinity)
        .map((a) => a.finished),
    ),
  )

/** Opens the drawer and follows its links, outermost first. */
async function drawer(page, ...names) {
  for (const name of names) {
    await page.getByRole('button', { name: 'Open navigation' }).click()
    await page
      .getByRole('navigation', { name: 'Main' })
      .getByRole('link', { name, exact: true })
      .click()
  }
}

/** Every page the drawer can reach: the items, then the pages under each. */
async function pages(page) {
  const links = async () => {
    await page.getByRole('button', { name: 'Open navigation' }).click()
    const nav = page.getByRole('navigation', { name: 'Main' })
    const hrefs = await nav.getByRole('link').evaluateAll((as) => as.map((a) => a.pathname))
    await page.keyboard.press('Escape')
    await expect(nav).toHaveCount(0)
    return hrefs
  }
  await page.goto('/')
  const items = await links()
  const all = new Set(items)
  for (const href of items) {
    await page.goto(href)
    for (const h of await links()) all.add(h)
  }
  // An item with pages redirects to its first one, which is listed anyway.
  return [...all].filter((h) => ![...all].some((o) => o !== h && o.startsWith(`${h}/`)))
}

test('no page or tab scrolls sideways at 360px', async ({ page }) => {
  test.setTimeout(300_000)
  await login(page)
  await page.setViewportSize({ width: 360, height: 640 })
  for (const path of await pages(page)) {
    await page.goto(path)
    await settle(page)
    expect.soft(await overflow(page), path).toBeLessThanOrEqual(1)
    const tabs = page.getByRole('tablist', { name: 'Sections' }).getByRole('tab')
    const n = await tabs.count()
    for (let i = 1; i < n; i++) {
      await tabs.nth(i).click()
      await settle(page)
      expect.soft(await overflow(page), `${path} tab ${i + 1}`).toBeLessThanOrEqual(1)
    }
  }
})

test('the drawer opens, takes you to a page, and closes', async ({ page }) => {
  await login(page)
  await page.getByRole('button', { name: 'Open navigation' }).click()
  const nav = page.getByRole('navigation', { name: 'Main' })
  await expect(nav).toBeVisible()
  await still(page)
  await page.screenshot({ path: shot('drawer') })
  await nav.getByRole('link', { name: 'Firewall', exact: true }).click()
  await expect(page).toHaveURL(/\/firewall\/rules$/)
  await expect(nav).toHaveCount(0)
  await drawer(page, 'NAT')
  await expect(page.getByRole('heading', { name: 'NAT', level: 1 })).toBeVisible()
})

test('a dialog is a sheet with its submit in reach', async ({ page }) => {
  await login(page)
  await drawer(page, 'Firewall')
  await page.getByRole('button', { name: 'Add rule' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  await still(page)
  const viewport = page.viewportSize()
  const box = await dialog.boundingBox()
  expect(box.x).toBe(0)
  expect(box.width).toBe(viewport.width)
  expect(Math.round(box.y + box.height)).toBe(viewport.height)
  const save = dialog.getByRole('button', { name: 'Save to draft' })
  await expect(save).toBeInViewport()
  expect((await save.boundingBox()).height).toBeGreaterThanOrEqual(44)
  await page.screenshot({ path: shot('rule-dialog') })
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toHaveCount(0)
})

test('the apply bar docks at the bottom with its buttons in reach', async ({ page }) => {
  await login(page)
  await drawer(page, 'System', 'General')
  const hostname = page.getByLabel('Hostname')
  const before = await hostname.inputValue()
  await hostname.fill(`${before}-phone`)
  const apply = page.getByRole('button', { name: /Apply with \d+s confirmation/ })
  const discard = page.getByRole('button', { name: 'Discard' })
  const viewport = page.viewportSize()
  for (const y of [0, 10_000]) {
    await page.evaluate((top) => window.scrollTo(0, top), y)
    for (const button of [apply, discard]) {
      await expect(button).toBeInViewport()
      const box = await button.boundingBox()
      expect(box.height).toBeGreaterThanOrEqual(44)
      expect(box.y).toBeGreaterThan(viewport.height / 2)
    }
  }
  await page.screenshot({ path: shot('apply-bar') })

  // An apply waiting for its confirmation takes the bar's place.
  await apply.click()
  const pending = page.getByRole('status')
  await expect(pending).toContainText('awaiting confirmation')
  await expect(pending.getByRole('button', { name: 'Revert now' })).toBeInViewport()
  await page.screenshot({ path: shot('pending') })
  await pending.getByRole('button', { name: 'Revert now' }).click()
  await expect(pending).toContainText('Reverted')
  await discard.click()
  await expect(hostname).toHaveValue(before)
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})

test('a list reads as a stack of labelled rows', async ({ page }) => {
  await login(page)
  await drawer(page, 'Interfaces')
  const table = page.getByRole('table').first()
  await expect(table.locator('thead')).toBeHidden()
  const row = table
    .locator('tbody tr')
    .filter({ has: page.locator('td[data-label="Zone"]') })
    .first()
  expect(await row.evaluate((tr) => getComputedStyle(tr).display)).toBe('block')
  const zone = row.locator('td[data-label="Zone"]')
  expect(await zone.evaluate((td) => getComputedStyle(td, '::before').content)).toBe('"Zone"')
  await page.screenshot({ path: shot('interfaces'), fullPage: true })
})

test('a tab strip keeps the open tab in view', async ({ page }) => {
  await login(page)
  // A link to the last tab: nothing clicks it, so the strip has to bring
  // it into view on its own.
  await page.goto('/services/proxy#events')
  const events = page.getByRole('tab', { name: 'Events' })
  await expect(events).toHaveAttribute('data-state', 'active')
  await expect(events).toBeInViewport({ ratio: 1 })
})

test('the main pages in both themes', async ({ page }) => {
  await login(page)
  for (const theme of ['light', 'dark']) {
    await page.evaluate((t) => localStorage.setItem('ostiole.theme', t), theme)
    for (const [name, path] of [
      ['dashboard', '/'],
      ['rules', '/firewall/rules'],
      ['leases', '/services/dhcp#leases'],
    ]) {
      await page.goto(path)
      await settle(page)
      await page.screenshot({ path: shot(`${name}-${theme}`), fullPage: true })
    }
  }
  await page.evaluate(() => localStorage.removeItem('ostiole.theme'))
})
