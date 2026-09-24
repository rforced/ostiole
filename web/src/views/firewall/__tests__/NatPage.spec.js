import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import NatPage from '@/views/firewall/NatPage.vue'

vi.mock('@/lib/api', () => ({
  api: { systemNat: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

const stubs = { ConfirmButton: true, AppDialog: true }

const masquerade = {
  zone: 'wan',
  interfaces: ['eth0'],
  source: 'any IPv4',
  destination: 'anywhere',
  keys: ['nat_postrouting/auto-nat:wan'],
}

function draft(mode, rules = []) {
  return {
    version: 6,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [],
    rules: [],
    nat: { outbound: { mode, rules } },
  }
}

async function mountPage(d) {
  const config = useConfigStore()
  config.draft = d
  config.loaded = true
  const wrapper = mount(NatPage, { global: { stubs } })
  await flushPromises()
  return { config, wrapper }
}

// The animated tables are stubbed, so their rows are not under a tbody here.
const rows = (wrapper) => wrapper.findAll('tr').filter((r) => r.find('td').exists())

describe('NatPage automatic rules', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.systemNat.mockResolvedValue([masquerade])
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  // What automatic mode writes used to be a sentence in the mode hint and
  // nothing else. It is listed now, locked, as pfSense lists its automatic
  // rules, and read for the draft rather than for what is applied.
  it('lists what automatic mode writes, locked', async () => {
    const { config, wrapper } = await mountPage(draft('automatic'))
    expect(api.systemNat).toHaveBeenCalledWith(config.draft)
    expect(wrapper.text()).toContain('Automatic rules')
    const row = rows(wrapper).find((r) => r.text().includes('any IPv4'))
    expect(row.text()).toContain('wan')
    expect(row.text()).toContain('eth0')
    expect(row.text()).toContain('anywhere')
    expect(row.text()).toContain('the interface address')
    expect(row.text()).toContain('locked')
    expect(row.find('button').exists()).toBe(false)
    // Automatic mode has nothing to add.
    expect(wrapper.text()).not.toContain('Add outbound rule')
  })

  it("puts them under the operator's rules in hybrid mode, where they apply after them", async () => {
    const { wrapper } = await mountPage(
      draft('hybrid', [
        {
          id: 'nat-mail',
          enabled: true,
          zone: 'wan',
          description: 'Mail keeps its address',
          source: ['192.168.1.25/32'],
          address: '203.0.113.25',
        },
      ]),
    )
    const text = rows(wrapper).map((r) => r.text())
    const own = text.findIndex((t) => t.includes('Mail keeps its address'))
    const auto = text.findIndex((t) => t.includes('any IPv4'))
    expect(own).toBeGreaterThan(-1)
    expect(auto).toBeGreaterThan(own)
  })

  // A draft that does not validate keeps the rows of the last one that
  // did, so rows from automatic mode can outlive a switch to manual.
  it.each(['manual', 'disabled'])(
    'lists none in %s mode, whatever the last read said',
    async (mode) => {
      const { wrapper } = await mountPage(draft(mode))
      expect(wrapper.text()).not.toContain('Automatic rules')
      expect(wrapper.text()).not.toContain('any IPv4')
    },
  )

  it('reads again once edits pause, and keeps the rows while the draft does not validate', async () => {
    vi.useFakeTimers()
    const { config, wrapper } = await mountPage(draft('automatic'))
    expect(api.systemNat).toHaveBeenCalledTimes(1)

    api.systemNat.mockRejectedValueOnce(new Error('invalid configuration'))
    config.draft.zones.push({ name: 'dmz' })
    config.draft.zones.push({ name: 'guest' })
    await flushPromises()
    expect(api.systemNat).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    // One read for the two edits, and it was given the draft.
    expect(api.systemNat).toHaveBeenCalledTimes(2)
    expect(api.systemNat).toHaveBeenLastCalledWith(config.draft)
    expect(rows(wrapper).some((r) => r.text().includes('any IPv4'))).toBe(true)
  })
})

// A viewer reads NAT the way everybody else does and changes none of it:
// no Add, each row opens to View, and the mode shows but will not move.
describe('NatPage for a viewer', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.systemNat.mockResolvedValue([masquerade])
  })

  it('offers nothing to change', async () => {
    useAuthStore().user = { username: 'watcher', role: 'viewer' }
    const d = draft('hybrid', [
      {
        id: 'nat-mail',
        enabled: true,
        zone: 'wan',
        source: ['192.168.1.25/32'],
        address: '203.0.113.25',
      },
    ])
    d.nat.portForwards = [
      {
        id: 'web',
        enabled: true,
        zone: 'wan',
        protocol: 'tcp',
        ports: ['443'],
        target: '10.0.0.5',
      },
    ]
    const { wrapper } = await mountPage(d)
    const labels = wrapper.findAll('button').map((b) => b.text())
    expect(labels.filter((l) => l.startsWith('Add'))).toEqual([])
    expect(labels).not.toContain('Edit')
    expect(labels.filter((l) => l === 'View')).toHaveLength(2)
    expect(wrapper.get('#ob-mode').attributes('disabled')).toBeDefined()
  })
})
