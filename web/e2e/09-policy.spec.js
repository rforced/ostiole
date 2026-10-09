import { expect, test } from './fixtures.js'
import { applyAndConfirm, login, shot } from './helpers.js'

test.describe.configure({ mode: 'serial' })

// Two gateways, one the WAN's DHCP router and one at a fixed address, then
// a group of the pair and a rule sent through the group.
test('group two gateways and route a rule through them', async ({ page }) => {
  await login(page)
  await page.goto('/routing')

  await page.getByRole('button', { name: 'Add gateway' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('wan1')
  await dialog.getByLabel('Description').fill('Fibre')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'wan1' })).toContainText('from DHCP')

  await page.getByRole('button', { name: 'Add gateway' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('wan2')
  await dialog.getByLabel('Description').fill('Backup line')
  await dialog.getByLabel('Gateway address').fill('198.51.100.1')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'wan2' })).toContainText('198.51.100.1')

  await page.getByRole('button', { name: 'Add group' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('failover')
  await dialog.getByLabel('Description').fill('Fibre first')
  await dialog.getByRole('button', { name: 'Add gateway' }).click()
  await dialog.getByRole('button', { name: 'Add gateway' }).click()
  await expect(dialog.getByLabel('Gateway', { exact: true })).toHaveCount(2)
  await dialog.getByLabel('Gateway', { exact: true }).first().selectOption('wan1')
  await dialog.getByLabel('Gateway', { exact: true }).last().selectOption('wan2')
  await dialog.getByLabel('Tier').first().fill('0')
  await dialog.getByLabel('Tier').last().fill('1')
  await expect(
    dialog.getByText('tier 0 has one gateway, then tier 1 has one gateway'),
  ).toBeVisible()
  await page.screenshot({ path: shot('70-gateway-group'), fullPage: true })
  await dialog.getByRole('button', { name: 'Save to draft' }).click()

  const group = page.getByRole('row').filter({ hasText: 'failover' })
  await expect(group).toContainText('wan1 → wan2')

  await applyAndConfirm(page)

  // Send a rule through the group.
  await page.goto('/firewall')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'lan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('Guests out the backup line')
  await dialog.getByLabel('Route through').selectOption('failover')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  const rule = page.getByRole('row').filter({ hasText: 'Guests out the backup line' })
  await expect(rule).toContainText('→ failover')
  await page.screenshot({ path: shot('71-policy-rule'), fullPage: true })

  await applyAndConfirm(page)

  // The ruleset gains a marking chain that runs before the routing decision.
  await page.goto('/diagnostics/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.locator('pre')
  await expect(ruleset).toContainText('chain policy_prerouting')
  await expect(ruleset).toContainText('hook prerouting priority mangle')
  // The mark is written into its own byte rather than over the whole
  // register, on the packet and on the connection alike, so traffic
  // shaping can keep a tier in the same word.
  await expect(ruleset).toContainText('meta mark set meta mark & 0xff00ffff | 0x')
  await expect(ruleset).toContainText('ct mark set ct mark & 0xff00ffff | 0x')
  await expect(ruleset).toContainText('ct mark & 0x00ff0000 != 0x0')

  // The routing page reports what the rule is pointed at.
  await page.goto('/routing')
  await expect(page.getByText('Policy routing is in use:')).toBeVisible()
  await page.screenshot({ path: shot('72-routing'), fullPage: true })
})

test('a gateway cannot be combined with a leaving zone', async ({ page }) => {
  await login(page)
  await page.goto('/firewall')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'lan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  const dialog = page.getByRole('dialog')

  const gateway = dialog.getByLabel('Route through')
  await gateway.selectOption('failover')
  // Picking the zone the traffic leaves by takes the choice away: the
  // outgoing interface is not known when the mark is set.
  await dialog.getByLabel('Leaving via zone').selectOption('wan')
  await expect(gateway).toBeDisabled()
  await expect(gateway).toHaveValue('')

  await dialog.getByLabel('Leaving via zone').selectOption('')
  await expect(gateway).toBeEnabled()
  // A rule that drops traffic has nowhere to send it either.
  await dialog.getByLabel('Action').selectOption('drop')
  await expect(gateway).toBeDisabled()

  await dialog.getByRole('button', { name: 'Cancel' }).click()
})

test('deleting a gateway warns that a group uses it', async ({ page }) => {
  await login(page)
  await page.goto('/routing')
  // Scoped to the gateway table: the group row mentions wan2 as well.
  const row = page
    .getByRole('region', { name: 'Gateways', exact: true })
    .getByRole('row')
    .filter({ hasText: 'wan2' })
  await row.getByRole('button', { name: 'Delete' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('group failover')
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
})

test('a default route the kernel already has is offered as a gateway', async ({ page }) => {
  await login(page)
  await page.goto('/routing')
  const gateways = page.getByRole('region', { name: 'Gateways', exact: true }).getByRole('table')
  const detected = page
    .getByRole('region', { name: 'Detected routes', exact: true })
    .getByRole('table')

  // This machine has a default route of its own; no configured gateway
  // covers it, so it is listed unwatched with an offer to adopt it.
  await expect(detected.getByText('not watched').first()).toBeVisible()
  const adopt = detected.getByRole('button', { name: /^Add as gw_/ }).first()
  const name = (await adopt.innerText()).replace('Add as ', '').trim()
  await page.screenshot({ path: shot('73-detected-gateway'), fullPage: true })
  await adopt.click()

  // It lands in the draft, and the route stops being offered because the
  // new gateway now claims it.
  const row = gateways.getByRole('row').filter({ hasText: name })
  await expect(row).toBeVisible()
  await expect(detected.getByRole('button', { name: `Add as ${name}` })).toHaveCount(0)
  await expect(detected.getByText(name).first()).toBeVisible()

  // Adopted from DHCP, so no address is pinned: the next lease would
  // otherwise break it.
  await row.getByRole('button', { name: 'Edit' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Gateway address')).toHaveValue('')
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByText('Unapplied changes.')).toHaveCount(0)
})

/** A provider's wg-quick file, keys from the wg(8) man page. */
const PROVIDER = `[Interface]
PrivateKey = yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=
Address = 10.64.1.2/32
DNS = 10.64.0.1
PostUp = iptables -A FORWARD -i %i -j ACCEPT

[Peer]
PublicKey = xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 198.51.100.7:51820
`

// A provider's file makes a way out: a tunnel in an external zone of its
// own and a gateway through it. A rule sent through the gateway brings in
// the masquerade into the tunnel and the kill switch that keeps its
// traffic off the WAN.
test('add a way out from a file and route a rule through it', async ({ page }) => {
  await login(page)
  await page.goto('/vpn/wireguard')
  await page.getByRole('button', { name: 'Add from file' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('File', { exact: true }).fill(PROVIDER)
  await expect(dialog).toContainText('Ignored: PostUp on line 5.')
  await expect(dialog).toContainText('wg0, new and external')
  await expect(dialog.getByLabel('Monitor address')).toHaveValue('10.64.0.1')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await applyAndConfirm(page)

  // The test server makes no devices, so the kernel has no tunnel to show.
  const tunnel = page
    .getByRole('region', { name: 'Tunnels' })
    .getByRole('row')
    .filter({ hasText: 'wg0' })
  await expect(tunnel.locator('.badge-warn')).toHaveText('down')
  await expect(tunnel.locator('[data-label="Gateway"]')).toContainText('wg0')

  await page.goto('/routing')
  await expect(
    page.getByRole('region', { name: 'Gateways' }).getByRole('row').filter({ hasText: 'wg0' }),
  ).toContainText('through the tunnel')
  await page.getByRole('tab', { name: 'Routes' }).click()
  // The tunnel is external like the WAN, so each answers what comes in on
  // it. The test server installs no tables, so neither has a next hop.
  const replies = page.getByRole('region', { name: 'Replies' })
  await expect(replies.getByRole('row')).toHaveCount(3)
  await expect(replies.getByRole('row').filter({ hasText: 'wg0' })).toContainText('default route')

  await page.goto('/firewall')
  await page.getByRole('group', { name: 'Zone' }).getByRole('button', { name: 'lan' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Description').fill('The TV out the provider')
  await dialog.getByLabel('Route through').selectOption('wg0')
  await dialog.getByRole('button', { name: 'Save to draft' }).click()
  await applyAndConfirm(page)

  await page.goto('/diagnostics/ruleset')
  await page.getByRole('button', { name: 'Show confirmed ruleset' }).click()
  const ruleset = page.locator('pre')
  await expect(ruleset).toContainText('comment "tunnel-nat:wg0"')
  await expect(ruleset).toContainText('comment "tunnel-mss"')
  await expect(ruleset).toContainText('comment "kill-switch:wg0"')
  await expect(ruleset).toContainText('comment "reply:wg0"')
})
