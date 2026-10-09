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
    expect(wrapper.text()).toContain('10,000 is the default. About 15.4 MB of memory when full.')
    expect(wrapper.text()).toContain('7 is the default, 365 at most.')
    // Only the firewall log is on in an empty draft: 50,000 of 350 bytes.
    expect(wrapper.text()).toContain('All logs: 17.5 MB of 8.0 GB when full.')
    expect(wrapper.text()).not.toContain('more than half')
  })

  it('writes the fields it is given, dropping an emptied one', async () => {
    const { wrapper, model } = await card({ settings: { days: 3 } })
    await wrapper.find('#events-entries').setValue('500')
    expect(model.value).toEqual({ days: 3, entries: 500 })
    await wrapper.find('#events-days').setValue('')
    expect(model.value).toEqual({ entries: 500 })
    expect(wrapper.text()).toContain('About 768 kB of memory when full.')
  })

  it('warns past half of the memory', async () => {
    const draft = {
      system: { management: { firewallLog: { entries: 1000000 } } },
    }
    const { wrapper } = await card({ draft, memTotal: 600_000_000 })
    expect(wrapper.text()).toContain('All logs: 350 MB of 600 MB when full.')
    expect(wrapper.text()).toContain("That is more than half of this router's memory.")
  })

  it("keeps the model's most and the memory when the limits fail", async () => {
    const { wrapper } = await card()
    expect(api.logLimits).toHaveBeenCalledTimes(1)
    expect(wrapper.find('#events-entries').attributes('max')).toBe('1000000')
    expect(wrapper.text()).not.toContain('This router allows')
    expect(wrapper.text()).toContain('All logs: 17.5 MB of 8.0 GB when full.')
  })

  it('caps the entries at what this router allows', async () => {
    const { wrapper } = await card({ log: 'firewall', memTotal: 4_000_000_000, limits: LIMITS })
    expect(wrapper.find('#firewall-entries').attributes('max')).toBe('6619047')
    expect(wrapper.text()).toContain(
      '50,000 is the default. About 17.5 MB of memory when full. This router allows up to 6,619,047.',
    )
  })

  it("says nothing more where this router allows the model's most", async () => {
    const { wrapper } = await card({ memTotal: 4_000_000_000, limits: LIMITS })
    expect(wrapper.find('#events-entries').attributes('max')).toBe('1000000')
    expect(wrapper.text()).toContain('10,000 is the default. About 15.4 MB of memory when full.')
    expect(wrapper.text()).not.toContain('This router allows')
  })

  it('sets every log against what this router has for them', async () => {
    let { wrapper } = await card({ memTotal: 4_000_000_000, limits: LIMITS })
    expect(wrapper.text()).toContain(
      'All logs: 26.3 MB at their largest. This router has 3.5 GB for them.',
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
      'All logs: 3.1 GB at their largest. This router has 3.5 GB for them.',
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
      'All logs: 3.5 GB at their largest. This router has 3.5 GB for them.',
    )
    expect(wrapper.text()).toContain('That is more than this router has for them.')
    expect(wrapper.text()).not.toContain('half')
  })

  // While System → General writes the logs to files, the card says what
  // this log's files hold and how long they keep it: the shorter of the
  // files' days and its own.
  it('says what the files keep while the logs are written to them', async () => {
    const draft = { system: { logging: { files: { enabled: true } } } }
    const files = { enabled: true, logs: [{ name: 'firewall', bytes: 1_200_000 }] }
    let { wrapper } = await card({ log: 'firewall', draft, files })
    expect(wrapper.text()).toContain('7 is the default, 365 at most. Files keep 7 days.')
    expect(wrapper.text()).toContain('In files: 1.2 MB.')
    ;({ wrapper } = await card({ log: 'firewall', draft, files, settings: { days: 20 } }))
    expect(wrapper.text()).toContain('Files keep 20 days.')
    ;({ wrapper } = await card({ log: 'firewall', draft, files, settings: { days: 90 } }))
    expect(wrapper.text()).toContain('Files keep 31 days.')
    // Traffic's destinations and the WAF events are kept in files too.
    ;({ wrapper } = await card({
      log: 'destinations',
      draft,
      files: { enabled: true, logs: [{ name: 'destinations', bytes: 3_000_000 }] },
    }))
    expect(wrapper.text()).toContain('Files keep 7 days.')
    expect(wrapper.text()).toContain('In files: 3.0 MB.')
    ;({ wrapper } = await card({
      log: 'events',
      draft,
      files: { enabled: true, logs: [{ name: 'events', bytes: 40_000 }] },
      settings: { days: 14 },
    }))
    expect(wrapper.text()).toContain('Files keep 14 days.')
    expect(wrapper.text()).toContain('In files: 40.0 kB.')
    expect(api.logFiles).toHaveBeenCalledTimes(5)
  })

  it('has nothing to size while the log is off', async () => {
    const { wrapper } = await card({ off: true })
    expect(wrapper.text()).toContain('Off.')
    expect(wrapper.find('#events-entries').exists()).toBe(false)
  })
})
