import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

import LogRetention from '@/components/LogRetention.vue'
import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'

vi.mock('@/lib/api', () => ({ api: { systemStats: vi.fn(), logFiles: vi.fn() } }))

async function card({
  settings = {},
  memTotal = 8_000_000_000,
  draft = {},
  off = false,
  log = 'events',
  files = { enabled: false, logs: [] },
} = {}) {
  api.systemStats.mockResolvedValue({ memTotal })
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
