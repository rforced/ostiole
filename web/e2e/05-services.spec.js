import { expect, test } from '@playwright/test'

import { applyAndConfirm, login, shot, sidebar } from './helpers.js'

test.describe.configure({ mode: 'serial' })

test('enable DHCP with a server and DNS with an override, then apply', async ({ page }) => {
  await login(page)
  await page.goto('/services')
  await expect(page).toHaveURL(/\/services\/dhcp$/)
  await expect(page.getByRole('heading', { name: 'DHCP', exact: true, level: 1 })).toBeVisible()

  // The wizard enabled DHCP on the LAN with a pool of hosts 100-199; edit that server.
  await expect(page.getByLabel('DHCP enabled')).toBeChecked()
  const serverRow = page.getByRole('row').filter({ hasText: '192.168.50.100' })
  await serverRow.getByRole('button', { name: 'Edit' }).click()
  let dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Range end')).toHaveValue('192.168.50.199')
  await dialog.getByLabel('Lease time').fill('1d')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(serverRow).toContainText('1d')

  // A second server offers only interfaces without one; the LAN is excluded.
  await page.getByRole('button', { name: 'Add server' }).click()
  dialog = page.getByRole('dialog')
  await expect(dialog.locator('#sc-if option', { hasText: '192.168.50.1/24' })).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  await page.getByRole('button', { name: 'Add static lease' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('MAC address').fill('AA:BB:CC:DD:EE:01')
  await dialog.getByLabel('IPv4 address').fill('192.168.50.20')
  await dialog.getByLabel('Hostname').fill('nas')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'aa:bb:cc:dd:ee:01' })).toContainText('nas')
  await page.screenshot({ path: shot('40-services-dhcp'), fullPage: true })

  await sidebar(page, 'DNS')
  await expect(page.getByLabel('DNS enabled')).toBeChecked()
  await page.getByLabel('Upstream resolvers').fill('1.1.1.1, 9.9.9.9')
  await page.getByLabel('Local domain').fill('lan')
  await page.getByRole('tab', { name: 'Overrides' }).click()
  await page.getByRole('button', { name: 'Add host' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Hostname').fill('printer')
  await dialog.getByLabel('Address').fill('192.168.50.30')
  await dialog.getByLabel('Aliases').fill('scanner')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const printer = page.getByRole('row').filter({ hasText: 'printer' })
  await expect(printer).toContainText('192.168.50.30')
  await expect(printer).toContainText('printer.lan')
  await expect(printer).toContainText('scanner')

  // A name with a domain of its own answers in full and nothing else.
  await page.getByRole('button', { name: 'Add host' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Hostname').fill('potato')
  await dialog.getByLabel('Domain').fill('test')
  await dialog.getByLabel('Address').fill('192.168.50.31')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'potato.test' })).toContainText(
    '192.168.50.31',
  )

  // A tailnet answers its own names, so that domain goes straight to it.
  await page.getByRole('button', { name: 'Add domain' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Domain').fill('ts.net')
  await dialog.getByLabel('Resolvers').fill('100.100.100.100')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'ts.net' })).toContainText('100.100.100.100')
  await page.screenshot({ path: shot('41-services-dns'), fullPage: true })

  await applyAndConfirm(page)

  // The page has its own URL and the tab is in it, so a reload comes back
  // to the DNS overrides, not to DHCP.
  await page.reload()
  await expect(page).toHaveURL(/\/services\/dns#overrides$/)
  await expect(page.getByRole('row').filter({ hasText: 'ts.net' })).toContainText('100.100.100.100')
  await page.getByRole('tab', { name: 'Resolver' }).click()
  await expect(page.getByLabel('Upstream resolvers')).toHaveValue('1.1.1.1, 9.9.9.9')
  await sidebar(page, 'DHCP')
  await expect(page.getByLabel('DHCP enabled')).toBeChecked()
  await page.getByRole('tab', { name: 'Leases' }).click()
  await expect(page.getByText('No leases.')).toBeVisible()
})

test('a pool outside the interface subnet is rejected by the server', async ({ page }) => {
  await login(page)
  await page.goto('/services')
  await page
    .getByRole('row')
    .filter({ hasText: '192.168.50.100' })
    .getByRole('button', { name: 'Edit' })
    .click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Range end').fill('10.9.9.9')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await page.getByRole('button', { name: /Apply with \d+s confirmation/ }).click()
  await expect(page.getByRole('alert')).toContainText('rangeEnd')
  await page.getByRole('button', { name: 'Discard' }).click()
})

test('advertise IPv6 on the LAN and pin a lease from the prefix', async ({ page }) => {
  await login(page)

  // DHCPv6 needs IPv6 on the interface; the wizard leaves the LAN v4-only.
  await page.goto('/interfaces')
  const lan = page.getByRole('row').filter({ hasText: '192.168.50.1/24' })
  await lan.getByRole('button', { name: 'Edit' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Mode').nth(1).selectOption('static')
  await dialog.getByLabel('Address (CIDR)').nth(1).fill('fd00:50::1/64')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(dialog).toHaveCount(0)

  // Navigate inside the SPA: a full page load would drop the draft.
  await page.getByRole('link', { name: 'Services' }).click()
  await page.getByRole('tab', { name: 'IPv6' }).click()
  await page.getByRole('button', { name: 'Advertise IPv6' }).click()
  dialog = page.getByRole('dialog')
  const lanOption = dialog.locator('#v6-if option', { hasText: 'IPv6 static' }).first()
  await dialog.getByLabel('Interface').selectOption(await lanOption.getAttribute('value'))
  await dialog.getByLabel('Mode').selectOption('managed')
  // Managed mode suggests the usual pool.
  await expect(dialog.getByLabel('Range start')).toHaveValue('::100')
  await dialog.getByLabel('Lease time').fill('6h')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const v6row = page.getByRole('row').filter({ hasText: '::100 – ::1ff' })
  await expect(v6row).toContainText('Managed DHCPv6')
  await page.screenshot({ path: shot('42-services-dhcpv6'), fullPage: true })

  // The static lease gains an address from the same prefix.
  await page.getByRole('tab', { name: 'IPv4' }).click()
  await page
    .getByRole('row')
    .filter({ hasText: 'nas' })
    .getByRole('button', { name: 'Edit' })
    .click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('IPv6 address').fill('::20')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'nas' })).toContainText('::20')

  await applyAndConfirm(page)

  await page.reload()
  await page.getByRole('tab', { name: 'IPv6' }).click()
  await expect(page.getByRole('row').filter({ hasText: '::100 – ::1ff' })).toContainText('6h')
})

test('switch the resolver to DNS over TLS', async ({ page }) => {
  await login(page)
  await page.goto('/services')
  await sidebar(page, 'DNS')

  // Forwarding is the default and shows plain upstreams.
  await expect(page.getByLabel('Upstream resolvers')).toBeVisible()
  // By role, not by label: the Resolver tab panel carries that name too.
  await page.getByRole('combobox', { name: 'Resolver', exact: true }).selectOption('tls')
  await expect(page.getByLabel('Upstream resolvers')).toHaveCount(0)
  const servers = page.getByLabel('DNS over TLS servers')
  await expect(servers).toHaveValue(/cloudflare-dns\.com/)
  await servers.fill('9.9.9.9 dns.quad9.net')
  await expect(page.getByRole('note').filter({ hasText: 'validating resolver' })).toContainText(
    'ostiole repair',
  )
  await page.screenshot({ path: shot('43-services-dot'), fullPage: true })

  await applyAndConfirm(page)

  await page.reload()
  await sidebar(page, 'DNS')
  await expect(page.getByRole('combobox', { name: 'Resolver', exact: true })).toHaveValue('tls')
  await expect(page.getByLabel('DNS over TLS servers')).toHaveValue('9.9.9.9 dns.quad9.net')
})
