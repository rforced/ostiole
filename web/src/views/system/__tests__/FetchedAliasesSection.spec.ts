import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import FetchedAliasesSection from '@/views/system/FetchedAliasesSection.vue'
import { configOf } from '@/test/fixtures'

const draft = (system = {}) =>
  configOf({ version: 16, system, zones: [], interfaces: [], rules: [] })

function open(system = {}) {
  const config = useConfigStore()
  config.saved = draft(system)
  config.replaceDraft(draft(system))
  return { wrapper: mount(FetchedAliasesSection), config }
}

vi.mock('@/lib/api', () => ({
  api: { logLimits: vi.fn(async () => ({ aliasEntries: 5000000 })) },
}))

describe('FetchedAliasesSection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('changes nothing by being opened', () => {
    const { config } = open({ maxmindLicenceKey: 'Abc0123456789xyz', maxAliasEntries: 3000000 })
    expect(config.dirty).toBe(false)
  })

  it('writes a trimmed key and leaves an emptied one out', async () => {
    const { wrapper, config } = open()
    const input = wrapper.get('#fa-maxmind-key')
    expect(wrapper.text()).not.toContain('GeoLite Data created by MaxMind')
    await input.setValue('  Abc0123456789xyz_mmk ')
    expect(config.draft.system.maxmindLicenceKey).toBe('Abc0123456789xyz_mmk')
    expect(wrapper.text()).toContain('GeoLite Data created by MaxMind')
    await input.setValue('   ')
    expect('maxmindLicenceKey' in config.draft.system).toBe(false)
    expect(config.dirty).toBe(false)
  })

  it('leaves the default limit out and says what a limit costs', async () => {
    const { wrapper, config } = open()
    const input = wrapper.get<HTMLInputElement>('#fa-max-entries')
    await flushPromises()
    expect(input.element.value).toBe('')
    expect(wrapper.text()).toContain('About 1.2 GB of memory')
    expect(wrapper.text()).toContain('Default 2,000,000, at most 5,000,000 on this router.')
    await input.setValue(3000000)
    expect(config.draft.system.maxAliasEntries).toBe(3000000)
    expect(wrapper.text()).toContain('About 1.8 GB')
    await input.setValue(2000000)
    expect('maxAliasEntries' in config.draft.system).toBe(false)
    await input.setValue('')
    expect(config.dirty).toBe(false)
  })

  it('lowers the default to what a small router allows', async () => {
    vi.mocked(api.logLimits).mockResolvedValueOnce({ aliasEntries: 791666 })
    const { wrapper } = open()
    await flushPromises()
    expect(wrapper.get('#fa-max-entries').attributes('placeholder')).toBe('791666')
    expect(wrapper.text()).toContain('Default 791,666, at most 791,666 on this router.')
  })
})
