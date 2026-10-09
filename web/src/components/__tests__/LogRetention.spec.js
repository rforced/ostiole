import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

import LogRetention from '@/components/LogRetention.vue'
import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'

vi.mock('@/lib/api', () => ({
  api: { systemStats: vi.fn(), logFiles: vi.fn(), logLimits: vi.fn() },
}))

// What a router of 4 GB answers: 3,475 MB for the logs at their largest,
// the firewall log and the requests held below the model's most.
const LIMITS = {
  memTotal: 4_000_000_000,
  reserve: 525_000_000,
  budget: 3_475_000_000,
  peakFactor: 1.5,
  ceilings: {
    firewall: 6_619_047,
    queries: 10_000_000,
    events: 1_000_000,
    destinations: 10_000_000,
    requests: 5_791_666,
    dhcp: 1_000_000,
    wireless: 1_000_000,
    wireguard: 1_000_000,
    tailscale: 1_000_000,
  },
}

/** limits null is a daemon that does not answer them. */
async function card({
  settings = {},
  memTotal = 8_000_000_000,
  limits = null,
  draft = {},
  off = false,
  log = 'events',
  files = { enabled: false, logs: [] },
} = {}) {
  api.systemStats.mockResolvedValue({ memTotal })
  if (limits) api.logLimits.mockResolvedValue(limits)
  else api.logLimits.mockRejectedValue(new Error('Not Found'))
  api.logFiles.mockResolvedValue(files)
  const store = useConfigStore()
  store.draft = { version: 11, system: {}, services: {}, ...draft }
  store.loaded = true
  const model = ref(settings)
  const wrapper = mount(LogRetention, {
    props: {
      log,
      title: 'WAF events',
      off,
      modelValue: model.value,
      'onUpdate:modelValue': (v) => {
        model.value = v
        wrapper.setProps({ modelValue: v })
      },
    },
  })
  await flushPromises()
  return { wrapper, model }
}

describe('LogRetention', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('quotes what the log costs full, and every log that is on against the memory', async () => {
    const { wrapper } = await card()
    expect(wrapper.text()).toContain('10,000 is the default. In memory, about 15.4 MB when full.')
    // Only the firewall log is on in an empty draft: 50,000 of 350 bytes.
    expect(wrapper.text()).toContain('All logs in memory: 17.5 MB of 8.0 GB when full.')
    expect(wrapper.text()).not.toContain('more than half')
  })

  // The days in memory are System › General's, one figure for every log.
  it('writes the entries, dropping them once emptied', async () => {
    const { wrapper, model } = await card()
    expect(wrapper.find('#events-days').exists()).toBe(false)
    await wrapper.find('#events-entries').setValue('500')
    expect(model.value).toEqual({ entries: 500 })
    expect(wrapper.text()).toContain('In memory, about 768 kB when full.')
    await wrapper.find('#events-entries').setValue('')
    expect(model.value).toEqual({})
  })

  it('warns past half of the memory', async () => {
    const draft = {
      system: { management: { firewallLog: { entries: 1000000 } } },
    }
    const { wrapper } = await card({ draft, memTotal: 600_000_000 })
    expect(wrapper.text()).toContain('All logs in memory: 350 MB of 600 MB when full.')
    expect(wrapper.text()).toContain("That is more than half of this router's memory.")
  })

  it("keeps the model's most and the memory when the limits fail", async () => {
    const { wrapper } = await card()
    expect(api.logLimits).toHaveBeenCalledTimes(1)
    expect(wrapper.find('#events-entries').attributes('max')).toBe('1000000')
    expect(wrapper.text()).not.toContain('This router allows')
    expect(wrapper.text()).toContain('All logs in memory: 17.5 MB of 8.0 GB when full.')
  })

  it('caps the entries at what this router allows', async () => {
    const { wrapper } = await card({ log: 'firewall', memTotal: 4_000_000_000, limits: LIMITS })
    expect(wrapper.find('#firewall-entries').attributes('max')).toBe('6619047')
    expect(wrapper.text()).toContain(
      'In memory, about 17.5 MB when full. Older entries are dropped. This router allows up to 6,619,047.',
    )
  })

  it("says nothing more where this router allows the model's most", async () => {
    const { wrapper } = await card({ memTotal: 4_000_000_000, limits: LIMITS })
    expect(wrapper.find('#events-entries').attributes('max')).toBe('1000000')
    expect(wrapper.text()).toContain('10,000 is the default. In memory, about 15.4 MB when full.')
    expect(wrapper.text()).not.toContain('This router allows')
  })

  it('sets every log against what this router has for them', async () => {
    let { wrapper } = await card({ memTotal: 4_000_000_000, limits: LIMITS })
    expect(wrapper.text()).toContain(
      'All logs in memory: 26.3 MB at their largest. This router has 3.5 GB for them.',
    )
    expect(wrapper.text()).not.toContain('That is more')
    // 2.1 GB is past half of the memory, but 3.15 GB at their largest fits.
    const firewallLog = (entries) => ({ system: { management: { firewallLog: { entries } } } })
    ;({ wrapper } = await card({
      memTotal: 4_000_000_000,
      limits: LIMITS,
      draft: firewallLog(6_000_000),
    }))
    expect(wrapper.text()).toContain(
      'All logs in memory: 3.1 GB at their largest. This router has 3.5 GB for them.',
    )
    expect(wrapper.text()).not.toContain('That is more')
    // Each log within its ceiling, together past 3,475 MB at their largest.
    ;({ wrapper } = await card({
      memTotal: 4_000_000_000,
      limits: LIMITS,
      draft: {
        ...firewallLog(6_619_047),
        services: { dns: { enabled: true, queryLog: { enabled: true } } },
      },
    }))
    expect(wrapper.text()).toContain(
      'All logs in memory: 3.5 GB at their largest. This router has 3.5 GB for them.',
    )
    expect(wrapper.text()).toContain('That is more than this router has for them.')
    expect(wrapper.text()).not.toContain('half')
  })

  // While System › General writes the logs to files, the card says what
  // this log's files hold and how long they keep it: the files' days.
  it('says what the files keep while the logs are written to them', async () => {
    const draft = (files) => ({ system: { logging: { files } } })
    const files = { enabled: true, logs: [{ name: 'firewall', bytes: 1_200_000 }] }
    let { wrapper } = await card({ log: 'firewall', draft: draft({ enabled: true }), files })
    expect(wrapper.text()).toContain(
      'In memory, about 17.5 MB when full. Older entries stay in the files for 30 days.',
    )
    expect(wrapper.text()).toContain('In files: 1.2 MB.')
    ;({ wrapper } = await card({
      log: 'firewall',
      draft: draft({ enabled: true, retentionDays: 90 }),
      files,
    }))
    expect(wrapper.text()).toContain('Older entries stay in the files for 90 days.')
    // Traffic's destinations and the WAF events are kept in files too.
    ;({ wrapper } = await card({
      log: 'destinations',
      draft: draft({ enabled: true, retentionDays: 14 }),
      files: { enabled: true, logs: [{ name: 'destinations', bytes: 3_000_000 }] },
    }))
    expect(wrapper.text()).toContain('Older entries stay in the files for 14 days.')
    expect(wrapper.text()).toContain('In files: 3.0 MB.')
    ;({ wrapper } = await card({
      log: 'events',
      draft: draft({ enabled: true }),
      files: { enabled: true, logs: [{ name: 'events', bytes: 40_000 }] },
    }))
    expect(wrapper.text()).toContain('Older entries stay in the files for 30 days.')
    expect(wrapper.text()).toContain('In files: 40.0 kB.')
    // Off, the files' days say nothing.
    ;({ wrapper } = await card({ log: 'firewall', draft: draft({ retentionDays: 90 }) }))
    expect(wrapper.text()).toContain(
      'In memory, about 17.5 MB when full. Older entries are dropped.',
    )
    expect(wrapper.text()).not.toContain('in the files')
    expect(wrapper.text()).not.toContain('In files:')
    expect(api.logFiles).toHaveBeenCalledTimes(5)
  })

  it('has nothing to size while the log is off', async () => {
    const { wrapper } = await card({ off: true })
    expect(wrapper.text()).toContain('Off.')
    expect(wrapper.find('#events-entries').exists()).toBe(false)
  })
})
