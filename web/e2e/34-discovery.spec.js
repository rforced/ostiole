import { expect, test } from './fixtures.js'
import { applyAndConfirm, confirmDialog, login, reconfigure, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// The relay needs two inside networks: the seed's LAN and the spare third
// link, put in a zone of its own here.
let lan = ''
let spare = ''

const header = (page) =>
  page.locator('header', {
    has: page.getByRole('heading', { name: 'Discovery', exact: true, level: 1 }),
  })

test('relay between two networks, apply, and read it back', async ({ page }) => {
  await login(page)
  const live = await (await page.request.get('/api/v1/interfaces/live')).json()
  await reconfigure(page.request, (config) => {
    lan = config.interfaces.find((i) => i.zone === 'lan').name
    const free = live.find(
      (l) =>
        l.kind !== 'loopback' && !l.wireless && !config.interfaces.some((i) => i.name === l.name),
    )
    spare = free.name
    config.zones.push({ name: 'gadgets', description: 'Speakers and screens' })
    config.interfaces.push({
      name: spare,
      zone: 'gadgets',
      description: 'Gadgets',
      enabled: true,
      ipv4: { mode: 'static', address: '10.77.0.1/24' },
      ipv6: { mode: 'none' },
    })
  })

  await page.goto('/services/discovery')
  await expect(
    page.getByRole('heading', { name: 'Discovery', exact: true, level: 1 }),
  ).toBeVisible()
  const enabled = page.getByLabel('Discovery enabled')
  await expect(enabled).not.toBeChecked()
  await enabled.check()

  // A first tick adds the network in both roles; the LAN only asks and the
  // gadgets only answer.
  await page.getByLabel(`${lan} asks`, { exact: true }).check()
  await expect(page.getByLabel(`${lan} answers`, { exact: true })).toBeChecked()
  await page.getByLabel(`${lan} answers`, { exact: true }).uncheck()
  await page.getByLabel(`${spare} answers`, { exact: true }).check()
  await expect(page.getByLabel(`${spare} asks`, { exact: true })).toBeChecked()
  await page.getByLabel(`${spare} asks`, { exact: true }).uncheck()
  await expect(page.getByLabel(`${spare} answers`, { exact: true })).toBeChecked()

  await page.locator('#discovery-services').fill('_flumph._tcp\n_quibble._udp')
  await page.screenshot({ path: shot('130-discovery'), fullPage: true })
  await applyAndConfirm(page)

  await page.reload()
  await expect(page.getByLabel('Discovery enabled')).toBeChecked()
  await expect(page.getByLabel(`${lan} asks`, { exact: true })).toBeChecked()
  await expect(page.getByLabel(`${lan} answers`, { exact: true })).not.toBeChecked()
  await expect(page.getByLabel(`${spare} asks`, { exact: true })).not.toBeChecked()
  await expect(page.getByLabel(`${spare} answers`, { exact: true })).toBeChecked()
  await expect(page.locator('#discovery-services')).toHaveValue('_flumph._tcp\n_quibble._udp')

  // Both networks may send to the group; replies come back only on the
  // one that answers.
  const res = await page.request.get('/api/v1/ruleset')
  expect(res.ok(), await res.text()).toBe(true)
  const lines = (await res.text()).split('\n').filter((l) => l.includes('service:discovery'))
  const mdns = lines.find((l) => l.includes('udp dport 5353'))
  expect(mdns).toContain(`"${lan}"`)
  expect(mdns).toContain(`"${spare}"`)
  const replies = lines.find((l) => l.includes('udp dport 61900-61999'))
  expect(replies).toContain(`"${spare}"`)
  expect(replies).not.toContain(`"${lan}"`)
})

test('the badge agrees with the relay', async ({ page }) => {
  await login(page)
  // Dummy links may refuse a multicast join, so the relay may report a
  // problem rather than run: the page must say whichever it is.
  await expect(async () => {
    await page.goto('/services/discovery')
    const res = await page.request.get('/api/v1/services/status')
    expect(res.ok()).toBe(true)
    const status = await res.json()
    await expect(header(page).locator('.badge')).toHaveText(
      status.discoveryRunning ? 'running' : 'stopped',
      { timeout: 2_000 },
    )
  }).toPass({ timeout: 20_000 })
})

test('the log and the announcements start empty, and Clear works', async ({ page }) => {
  await login(page)
  await page.goto('/services/discovery')
  await page.getByRole('tab', { name: 'Log' }).click()
  await expect(page.getByText('No packets.')).toBeVisible()
  await expect(page.getByText('Reading…')).toHaveCount(0)

  await page.getByRole('button', { name: 'Clear', exact: true }).click()
  await confirmDialog(page, { confirm: 'Clear' })
  await expect(page.getByText('No packets.')).toBeVisible()
  await expect(page.getByRole('alert')).toHaveCount(0)

  await page.getByRole('tab', { name: 'Announcements' }).click()
  await expect(page.getByText('No announcements.')).toBeVisible()
})

test('a relay left with one network is refused', async ({ page }) => {
  await login(page)
  await page.goto('/services/discovery')
  // Clearing the last role takes the network out of the relay.
  await page.getByLabel(`${spare} answers`, { exact: true }).uncheck()
  await expect(page.getByLabel(`${spare} asks`, { exact: true })).not.toBeChecked()
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  await expect(page.getByRole('alert')).toContainText('the relay needs at least two interfaces')

  await page.getByLabel(`${spare} answers`, { exact: true }).check()
  await page.getByLabel(`${spare} asks`, { exact: true }).uncheck()
  await applyAndConfirm(page)
})
