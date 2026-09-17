import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import { api } from '@/lib/api'
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

async function mountPage(path = '/firewall/rules') {
  api.config.get.mockResolvedValue(structuredClone(config))
  await useConfigStore().load()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/firewall/rules', component: RulesPage }],
  })
  router.push(path)
  await router.isReady()
  const wrapper = mount(RulesPage, { global: { plugins: [router], stubs } })
  await vi.waitFor(() => expect(api.systemRules).toHaveBeenCalled())
  await nextTick()
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
