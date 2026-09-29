import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import { api } from '@/lib/api'
import { formatCount } from '@/lib/format'
import { useConfigStore } from '@/stores/config'
import RulesPage from '@/views/firewall/RulesPage.vue'

vi.mock('@/lib/api', () => ({
  api: {
    counters: vi.fn(),
    systemRules: vi.fn(),
    config: { get: vi.fn(), diff: vi.fn() },
  },
  ApiError: class ApiError extends Error {},
}))

const stubs = { ConfirmButton: true, RuleDialog: true, RouterLink: true }

const config = {
  version: 3,
  zones: [
    { name: 'lan', antiLockout: true },
    { name: 'wan', external: true },
  ],
  interfaces: [
    { name: 'eth1', zone: 'lan', enabled: true },
    { name: 'eth0', zone: 'wan', enabled: true },
  ],
  rules: [
    {
      id: 'r-web',
      zone: 'lan',
      enabled: true,
      action: 'accept',
      protocol: 'tcp',
      source: {},
      destination: { self: true, ports: ['443'] },
      description: 'Web UI',
    },
  ],
}

// What the server says for this configuration: two baseline rows for every
// zone, anti-lockout on lan only, and a closing drop per zone.
const system = [
  {
    chain: 'input',
    action: 'accept',
    protocol: 'any',
    source: 'any',
    destination: 'any',
    description: 'Replies and related traffic of connections already allowed',
  },
  {
    chain: 'input',
    zones: ['lan'],
    action: 'accept',
    protocol: 'tcp',
    source: 'any',
    destination: 'this firewall : 443, 22',
    description: 'Anti-lockout, keeps the web UI and SSH reachable',
    keys: ['input/anti-lockout:lan'],
    setting: 'zone',
  },
  {
    chain: 'input',
    zones: ['wan'],
    action: 'drop',
    protocol: 'any',
    source: 'bogon networks',
    destination: 'any',
    description: 'Block bogon sources, set on eth0',
    log: true,
    keys: ['input/block-bogons', 'forward/block-bogons'],
    setting: 'interface',
  },
  {
    chain: 'zone_lan',
    after: true,
    zones: ['lan'],
    action: 'drop',
    protocol: 'any',
    source: 'any',
    destination: 'any',
    description: 'Everything else',
    keys: ['zone_lan/zone-unmatched'],
    setting: 'zone',
  },
  {
    chain: 'zone_wan',
    after: true,
    zones: ['wan'],
    action: 'drop',
    protocol: 'any',
    source: 'any',
    destination: 'any',
    description: 'Everything else',
    log: true,
    keys: ['zone_wan/zone-unmatched'],
    setting: 'zone',
  },
]

async function mountPage(path = '/firewall/rules', extraStubs = {}, cfg = config) {
  api.config.get.mockResolvedValue(structuredClone(cfg))
  await useConfigStore().load()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/firewall/rules', component: RulesPage }],
  })
  router.push(path)
  await router.isReady()
  const wrapper = mount(RulesPage, {
    global: { plugins: [router], stubs: { ...stubs, ...extraStubs } },
  })
  await vi.waitFor(() => expect(api.systemRules).toHaveBeenCalled())
  await flushPromises()
  return wrapper
}

// The body rows: the transition group is stubbed, so there is no tbody to ask.
const rowsText = (wrapper) =>
  wrapper
    .findAll('tr')
    .filter((tr) => tr.find('td').exists())
    .map((tr) => tr.text())
const systemRows = (wrapper) => wrapper.findAll('tr[data-system]')

describe('RulesPage system rules', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.counters.mockResolvedValue({
      'input/anti-lockout:lan': { packets: 7, bytes: 700 },
      'input/block-bogons': { packets: 2, bytes: 200 },
      'forward/block-bogons': { packets: 3, bytes: 300 },
    })
    api.systemRules.mockResolvedValue(system)
    api.config.diff.mockResolvedValue([])
  })

  it('shows the zone rows around the operator rules, in evaluation order', async () => {
    const wrapper = await mountPage()
    const rows = rowsText(wrapper)
    expect(rows[0]).toContain('Replies and related traffic')
    expect(rows[1]).toContain('Anti-lockout')
    expect(rows[2]).toContain('Web UI')
    expect(rows.at(-1)).toContain('Everything else')
    // The wan-only row stays off the lan tab.
    expect(rows.join('\n')).not.toContain('bogon')
  })

  it('offers no editing on a system rule, only the setting behind it', async () => {
    const wrapper = await mountPage()
    const lockout = systemRows(wrapper).find((tr) => tr.text().includes('Anti-lockout'))
    expect(lockout.find('input[type=checkbox]').exists()).toBe(false)
    expect(lockout.find('button').exists()).toBe(false)
    expect(lockout.find('router-link-stub').attributes('to')).toBe('/interfaces#zones')
    // The baseline has no setting, so nothing to link to.
    const replies = systemRows(wrapper).find((tr) => tr.text().includes('Replies'))
    expect(replies.find('router-link-stub').exists()).toBe(false)
  })

  it('links the kill switch of a blocking group to Routing', async () => {
    api.systemRules.mockResolvedValue([
      ...system,
      {
        chain: 'forward',
        zones: ['lan'],
        action: 'drop',
        protocol: 'any',
        source: 'routed through vpn',
        destination: 'out eth0',
        description: "Keep a blocking group's traffic off every other WAN",
        keys: ['forward/kill-switch:vpn'],
        setting: 'routing',
      },
    ])
    const wrapper = await mountPage()
    const row = systemRows(wrapper).find((tr) => tr.text().includes('routed through vpn'))
    expect(row.find('router-link-stub').attributes('to')).toBe('/routing')
  })

  it('sums the counters of a rule both base chains carry', async () => {
    const wrapper = await mountPage('/firewall/rules#wan')
    await vi.waitFor(() => expect(api.counters).toHaveBeenCalled())
    await nextTick()
    const bogons = systemRows(wrapper).find((tr) => tr.text().includes('bogon'))
    expect(bogons.text()).toContain('5')
    expect(bogons.text()).toContain('log')
    // A rule without a counter shows nothing rather than zero.
    const replies = systemRows(wrapper).find((tr) => tr.text().includes('Replies'))
    expect(replies.find('td.tabular-nums').text()).toBe('')
  })

  it('says how many a sampled log kept when it kept fewer than it counted', async () => {
    api.counters.mockResolvedValue({
      'input/block-bogons': { packets: 2, bytes: 200 },
      'forward/block-bogons': { packets: 3, bytes: 300 },
      'input/log:block-bogons': { packets: 1, bytes: 100 },
      'forward/log:block-bogons': { packets: 1, bytes: 100 },
      'r-scan': { packets: 900, bytes: 9000 },
      'log:r-scan': { packets: 40, bytes: 400 },
      'r-quiet': { packets: 12, bytes: 120 },
      'log:r-quiet': { packets: 12, bytes: 120 },
    })
    api.systemRules.mockResolvedValue(
      system.map((s) =>
        s.keys?.includes('input/block-bogons')
          ? { ...s, logKeys: ['input/log:block-bogons', 'forward/log:block-bogons'] }
          : s,
      ),
    )
    const drop = (id) => ({
      id,
      zone: 'wan',
      enabled: true,
      action: 'drop',
      protocol: 'tcp',
      source: {},
      destination: { ports: ['23'] },
      log: true,
    })
    const wrapper = await mountPage(
      '/firewall/rules#wan',
      {},
      {
        ...config,
        rules: [...config.rules, drop('r-scan'), drop('r-quiet')],
      },
    )
    await vi.waitFor(() => expect(api.counters).toHaveBeenCalled())
    await nextTick()
    const bogons = systemRows(wrapper).find((tr) => tr.text().includes('bogon'))
    expect(bogons.text()).toContain('2 logged')
    const rows = rowsText(wrapper)
    expect(rows.find((t) => t.includes('900'))).toContain('40 logged')
    // A log that kept every packet has nothing to add.
    expect(rows.find((t) => t.includes('12'))).not.toContain('logged')
  })

  it('groups the thousands of a count, as the dashboard does', async () => {
    api.counters.mockResolvedValue({
      'input/block-bogons': { packets: 2_000, bytes: 0 },
      'forward/block-bogons': { packets: 1_412, bytes: 0 },
      'input/log:block-bogons': { packets: 1_000, bytes: 0 },
      'forward/log:block-bogons': { packets: 1_116, bytes: 0 },
      'r-scan': { packets: 274_551, bytes: 0 },
      'log:r-scan': { packets: 61_207, bytes: 0 },
    })
    api.systemRules.mockResolvedValue(
      system.map((s) =>
        s.keys?.includes('input/block-bogons')
          ? { ...s, logKeys: ['input/log:block-bogons', 'forward/log:block-bogons'] }
          : s,
      ),
    )
    const scan = {
      id: 'r-scan',
      zone: 'wan',
      enabled: true,
      action: 'drop',
      protocol: 'tcp',
      source: {},
      destination: { ports: ['23'] },
      log: true,
    }
    const wrapper = await mountPage(
      '/firewall/rules#wan',
      {},
      { ...config, rules: [...config.rules, scan] },
    )
    await vi.waitFor(() => expect(api.counters).toHaveBeenCalled())
    await nextTick()
    const bogons = systemRows(wrapper).find((tr) => tr.text().includes('bogon'))
    expect(bogons.text()).toContain(formatCount(3_412))
    expect(bogons.text()).toContain(`${formatCount(2_116)} logged`)
    const scanRow = rowsText(wrapper).find((t) => t.includes(formatCount(274_551)))
    expect(scanRow).toContain(`${formatCount(61_207)} logged`)
  })

  it('opens the zone the hash names, so a reload comes back to it', async () => {
    const wrapper = await mountPage('/firewall/rules#wan')
    expect(wrapper.find('[aria-label="Zone"] [aria-pressed="true"]').text()).toBe('wan')
    expect(rowsText(wrapper).join('\n')).toContain('bogon')
    // The lan rule belongs to the other zone, and the hash is what says so.
    expect(rowsText(wrapper).join('\n')).not.toContain('Web UI')
  })

  it('falls back to the first zone for a hash no zone answers to', async () => {
    const wrapper = await mountPage('/firewall/rules#nonsense')
    expect(wrapper.find('[aria-label="Zone"] [aria-pressed="true"]').text()).toBe('lan')
  })

  it('writes the zone to the hash when you pick one', async () => {
    const wrapper = await mountPage()
    expect(wrapper.vm.$route.hash).toBe('')
    await wrapper.find('[aria-label="Zone"] button:last-child').trigger('click')
    await flushPromises()
    expect(wrapper.vm.$route.hash).toBe('#wan')
    expect(wrapper.find('[aria-label="Zone"] [aria-pressed="true"]').text()).toBe('wan')
    // Back on the default zone the URL is the plain page again.
    await wrapper.find('[aria-label="Zone"] button:first-child').trigger('click')
    await flushPromises()
    expect(wrapper.vm.$route.fullPath).toBe('/firewall/rules')
  })

  it('reads the system rules before showing any row', async () => {
    let answer
    api.systemRules.mockReturnValue(new Promise((resolve) => (answer = resolve)))
    const wrapper = await mountPage()
    // The lan rule is in the draft already, but on its own it would be
    // pushed down by the rows that arrive above it.
    expect(rowsText(wrapper)).toEqual(['Reading…'])
    answer(system)
    await flushPromises()
    const rows = rowsText(wrapper)
    expect(rows[0]).toContain('Replies and related traffic')
    expect(rows[2]).toContain('Web UI')
  })

  it('shows the zone rules alone when the system rules cannot be read', async () => {
    api.systemRules.mockRejectedValue(new Error('the draft does not validate'))
    const wrapper = await mountPage()
    expect(rowsText(wrapper)).toEqual([expect.stringContaining('Web UI')])
  })

  it('swaps the rows at once for another zone', async () => {
    const wrapper = await mountPage('/firewall/rules', { 'transition-group': false })
    await wrapper.find('[aria-label="Zone"] button:last-child').trigger('click')
    await flushPromises()
    // No lan row is left fading out beside the wan rows, and the wan rows
    // do not fade in.
    const body = wrapper.find('tbody')
    expect(body.text()).toContain('bogon')
    expect(body.text()).not.toContain('Web UI')
    expect(
      body.findAll('tr').filter((tr) => /row-(enter|leave)/.test(tr.classes().join(' '))),
    ).toEqual([])
  })

  it('re-reads the rows for the draft after an edit settles', async () => {
    const wrapper = await mountPage()
    expect(api.systemRules).toHaveBeenCalledTimes(1)
    const store = useConfigStore()
    store.upsertZone({ name: 'lan', antiLockout: false })
    store.upsertZone({ name: 'lan', antiLockout: true })
    await vi.waitFor(() => expect(api.systemRules).toHaveBeenCalledTimes(2))
    // One read for the two edits, and it was given the draft, not the saved state.
    expect(api.systemRules).toHaveBeenLastCalledWith(store.draft)
    wrapper.unmount()
  })
})
