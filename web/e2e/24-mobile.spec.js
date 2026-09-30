import { createPrivateKey, createPublicKey } from 'node:crypto'

import { expect, test } from './fixtures.js'
import { login, PASSWORD, reconfigure, sidebar } from './helpers.js'

// The phone layout, on a router filled with made-up rows (fill below).
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

/** Opens the drawer and follows it to a page, its section first. */
async function drawer(page, ...names) {
  await page.getByRole('button', { name: 'Open navigation' }).click()
  await sidebar(page, ...names)
}

/** Every page the drawer can reach: the items, then the pages of each section. */
async function pages(page) {
  await page.goto('/')
  await page.getByRole('button', { name: 'Open navigation' }).click()
  const nav = page.getByRole('navigation', { name: 'Main' })
  const links = () => nav.getByRole('link').evaluateAll((as) => as.map((a) => a.pathname))
  const all = new Set(await links())
  // One section is open at a time, so each is opened in turn.
  for (const section of await nav.getByRole('button').all()) {
    await section.click()
    await expect(section).toHaveAttribute('aria-expanded', 'true')
    for (const h of await links()) all.add(h)
  }
  await page.keyboard.press('Escape')
  await expect(nav).toHaveCount(0)
  return [...all]
}

/** A WireGuard key pair grown from one repeated byte: a key nobody holds. */
function keyPair(byte) {
  const secret = Buffer.alloc(32, byte)
  const pkcs8 = Buffer.concat([Buffer.from('302e020100300506032b656e04220420', 'hex'), secret])
  const spki = createPublicKey(createPrivateKey({ key: pkcs8, format: 'der', type: 'pkcs8' }))
  return {
    privateKey: secret.toString('base64'),
    publicKey: spki.export({ format: 'der', type: 'spki' }).subarray(-32).toString('base64'),
  }
}

/**
 * Made-up rows for the pages to lay out, long values among them, since
 * those are what overflow. Addresses outside the LAN come from the
 * documentation ranges (RFC 5737, RFC 3849), names from the reserved ones
 * (RFC 2606), MACs from the documentation block (RFC 7042), and nothing
 * here makes the router fetch or send anything.
 */
function fill(config) {
  const lan = config.interfaces.find((i) => i.zone === 'lan').name
  const wan = config.interfaces.find((i) => i.zone === 'wan').name
  config.system.logging = { ...config.system.logging, files: { enabled: true } }
  config.zones.push({ name: 'vpn', description: 'Laptops and phones away from home' })
  config.interfaces.push({
    name: 'wg0',
    zone: 'vpn',
    description: 'Road warriors',
    enabled: true,
    ipv4: { mode: 'static', address: '10.66.0.1/24' },
    ipv6: { mode: 'none' },
    mtu: 1420,
    wireguard: {
      ...keyPair(1),
      listenPort: 51820,
      peers: [
        {
          name: 'laptop',
          enabled: true,
          publicKey: keyPair(2).publicKey,
          allowedIps: ['10.66.0.2/32'],
        },
        {
          name: 'phone',
          description: 'The one in the blue case',
          enabled: true,
          publicKey: keyPair(3).publicKey,
          presharedKey: Buffer.alloc(32, 4).toString('base64'),
          allowedIps: ['10.66.0.3/32'],
        },
      ],
    },
  })
  config.aliases = [
    {
      name: 'office',
      type: 'hosts',
      description: 'The branch office and its IPv6 prefix',
      entries: ['198.51.100.0/24', '2001:db8:4f2a:1c00::/56'],
    },
    { name: 'mail', type: 'ports', entries: ['25', '465', '587', '993'] },
  ]
  config.schedules = [
    {
      name: 'evenings',
      description: 'School nights',
      days: ['sunday', 'monday', 'tuesday', 'wednesday', 'thursday'],
      start: '19:00',
      end: '22:30',
    },
  ]
  config.gateways = [
    {
      name: 'fibre',
      description: 'Fibre through the box in the hall cupboard',
      enabled: true,
      interface: wan,
      monitor: '192.0.2.1',
    },
    {
      name: 'lte',
      description: 'Backup line',
      enabled: true,
      interface: wan,
      address: '198.51.100.1',
      priority: 1,
    },
  ]
  config.gatewayGroups = [
    {
      name: 'failover',
      description: 'Fibre first',
      enabled: true,
      members: [{ gateway: 'fibre' }, { gateway: 'lte', tier: 1 }],
      onDown: 'fallback',
    },
  ]
  config.rules = [
    {
      id: 'r-office',
      description: 'The branch office reaches the file server over SMB and nothing else',
      enabled: true,
      zone: 'wan',
      action: 'accept',
      protocol: 'tcp',
      source: { alias: 'office' },
      destination: { addresses: ['192.168.50.20'], ports: ['445'] },
      log: true,
    },
    {
      id: 'r-mail',
      description: 'Mail',
      enabled: true,
      zone: 'wan',
      action: 'accept',
      protocol: 'tcp',
      source: {},
      destination: { addresses: ['192.168.50.25'], portAlias: 'mail' },
    },
    {
      id: 'r-evenings',
      description: 'No games on school nights',
      enabled: true,
      zone: 'lan',
      action: 'drop',
      protocol: 'any',
      source: { addresses: ['192.168.50.64/26'] },
      destination: {},
      schedule: 'evenings',
    },
    {
      id: 'r-guests',
      description: 'Guests out the backup line',
      enabled: true,
      zone: 'lan',
      action: 'accept',
      protocol: 'any',
      source: { addresses: ['192.168.50.192/26'] },
      destination: {},
      gateway: 'failover',
    },
    ...config.rules,
  ]
  config.nat = {
    ...config.nat,
    portForwards: [
      {
        id: 'pf-web',
        description: 'Web server',
        enabled: true,
        zone: 'wan',
        protocol: 'tcp',
        ports: ['80', '443'],
        target: '192.168.50.10',
        reflection: true,
      },
    ],
    oneToOne: [
      {
        id: 'one-mail',
        description: 'Mail server',
        enabled: true,
        zone: 'wan',
        external: '203.0.113.25',
        internal: '192.168.50.25',
      },
    ],
  }
  config.routes = [
    { id: 'rt-lab', enabled: true, destination: '10.200.0.0/16', gateway: '192.168.50.254' },
  ]
  const { dhcp, dns } = config.services
  dhcp.staticLeases = [
    { mac: '00:00:5e:00:53:01', ip: '192.168.50.20', hostname: 'nas' },
    {
      mac: '00:00:5e:00:53:02',
      ip: '192.168.50.21',
      hostname: 'television-in-the-upstairs-sitting-room',
    },
  ]
  dns.hostOverrides = [{ hostname: 'printer', ip: '192.168.50.30', aliases: ['scanner'] }]
  dns.domainOverrides = [{ domain: 'corp.example.com', servers: ['198.51.100.53', '2001:db8::53'] }]
  config.services.wol = {
    devices: [
      { id: 'wol-nas', interface: lan, mac: '00:00:5e:00:53:01', description: 'NAS in the loft' },
    ],
  }
  config.crons = [
    ...(config.crons ?? []),
    {
      id: 'cron-wake',
      description: 'Wake the NAS before its backup',
      enabled: true,
      schedule: '30 3 * * *',
      kind: 'wake',
      device: 'wol-nas',
    },
  ]
}

let filled = false

// The first test fills the router, through a session of its own so the
// page still starts signed out; every test then signs in.
test.beforeEach(async ({ page, playwright, baseURL }) => {
  if (!filled) {
    const api = await playwright.request.newContext({ baseURL })
    const signedIn = await api.post('/api/v1/auth/login', {
      data: { username: 'admin', password: PASSWORD },
      headers: { 'X-Requested-With': 'ostiole' },
    })
    expect(signedIn.ok()).toBe(true)
    await reconfigure(api, fill)
    await api.dispose()
    filled = true
  }
  await login(page)
})

test('no page or tab scrolls sideways at 360px', async ({ page }) => {
  test.setTimeout(300_000)
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
  await page.getByRole('button', { name: 'Open navigation' }).click()
  const nav = page.getByRole('navigation', { name: 'Main' })
  await expect(nav).toBeVisible()
  await still(page)
  await page.screenshot({ path: shot('drawer') })
  // A section opens its pages in place, and the drawer waits for the pick.
  await nav.getByRole('button', { name: 'Firewall', exact: true }).click()
  await expect(page).toHaveURL(/\/$/)
  await nav.getByRole('link', { name: 'Rules', exact: true }).click()
  await expect(page).toHaveURL(/\/firewall\/rules$/)
  await expect(nav).toHaveCount(0)

  // The section and the page you are on stand out from their neighbours.
  await page.getByRole('button', { name: 'Open navigation' }).click()
  const style = (el, prop) => el.evaluate((e, p) => getComputedStyle(e)[p], prop)
  const firewall = nav.getByRole('button', { name: 'Firewall', exact: true })
  const services = nav.getByRole('button', { name: 'Services', exact: true })
  expect(await style(firewall, 'color')).not.toBe(await style(services, 'color'))
  const rules = nav.getByRole('link', { name: 'Rules', exact: true })
  const nat = nav.getByRole('link', { name: 'NAT', exact: true })
  await expect(rules).toHaveAttribute('aria-current', 'page')
  expect(await style(rules, 'borderLeftColor')).not.toBe(await style(nat, 'borderLeftColor'))
  await nat.click()
  await expect(page.getByRole('heading', { name: 'NAT', level: 1 })).toBeVisible()
})

test('a dialog is a sheet with its submit in reach', async ({ page }) => {
  await drawer(page, 'Firewall', 'Rules')
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

// The log files are on (fill), so their settings show, one above the
// next.
test('the log files settings stack', async ({ page }) => {
  await drawer(page, 'System', 'General')
  const files = page
    .getByRole('region', { name: 'Logs', exact: true })
    .getByRole('group', { name: 'Files' })
  const days = await files.getByLabel('Kept for (days)').boundingBox()
  const every = await files.getByLabel('Write every').boundingBox()
  expect(every.y).toBeGreaterThanOrEqual(days.y + days.height)
  await files.scrollIntoViewIfNeeded()
  await page.screenshot({ path: shot('log-files') })
})

test('the apply bar docks at the bottom with its buttons in reach', async ({ page }) => {
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
  // A link to the last tab: nothing clicks it, so the strip has to bring
  // it into view on its own.
  await page.goto('/services/proxy#requests')
  const requests = page.getByRole('tab', { name: 'Requests' })
  await expect(requests).toHaveAttribute('data-state', 'active')
  await expect(requests).toBeInViewport({ ratio: 1 })
})

test('the main pages in both themes', async ({ page }) => {
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
