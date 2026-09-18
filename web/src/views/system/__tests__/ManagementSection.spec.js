import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import ManagementSection from '@/views/system/ManagementSection.vue'

vi.mock('@/lib/api', () => ({ api: { timezones: vi.fn() } }))

const draft = (system = {}) => ({ version: 3, system, zones: [], interfaces: [], rules: [] })

async function open(system) {
  const config = useConfigStore()
  config.replaceDraft(draft(system))
  const wrapper = mount(ManagementSection, { global: { stubs: { RouterLink: true } } })
  await flushPromises()
  return { wrapper, config, zone: wrapper.get('#sys-timezone') }
}

describe('ManagementSection timezone', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.timezones.mockResolvedValue({
      zones: ['UTC', 'Asia/Tokyo', 'Europe/Berlin'],
      current: 'UTC',
    })
  })

  it('shows UTC when the configuration says nothing', async () => {
    const { zone } = await open({})
    expect(zone.element.value).toBe('UTC')
  })

  it('writes a chosen zone into the draft', async () => {
    const { config, zone } = await open({ timezone: 'UTC' })
    await zone.setValue('Europe/Berlin')
    expect(config.draft.system.timezone).toBe('Europe/Berlin')
  })

  // Opening the page is not an edit: a draft that had no timezone still has
  // none until somebody picks one, so the apply bar stays quiet.
  it('does not touch a draft it only displays', async () => {
    const { config } = await open({})
    expect(config.draft.system.timezone).toBeUndefined()
  })

  // A configuration restored from a router with a bigger zone database must
  // not be quietly moved to whichever zone happens to sort next to it.
  it('keeps a zone the router does not offer', async () => {
    const { zone } = await open({ timezone: 'Pacific/Chatham' })
    expect(zone.element.value).toBe('Pacific/Chatham')
    expect(zone.findAll('option').map((o) => o.element.value)).toContain('Pacific/Chatham')
  })

  it('says what the clock reads while the setting is unapplied', async () => {
    const { wrapper } = await open({ timezone: 'Europe/Berlin' })
    expect(wrapper.text()).toContain('The clock reads UTC until you apply.')
  })
})

describe('ManagementSection host settings', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.timezones.mockResolvedValue({ zones: ['UTC'], current: 'UTC' })
  })

  it('writes the journal ceiling into the draft and clears it when emptied', async () => {
    const { wrapper, config } = await open({})
    const field = wrapper.get('#sys-journal')
    expect(field.element.value).toBe('')
    expect(field.attributes('placeholder')).toBe('10')
    await field.setValue('25')
    expect(config.draft.system.journalMaxUseGB).toBe(25)
    await field.setValue('')
    expect(config.draft.system).not.toHaveProperty('journalMaxUseGB')
  })

  it('adds management rules for an external zone as ordinary rules', async () => {
    const config = useConfigStore()
    config.replaceDraft({
      version: 3,
      system: { management: { webPort: 443, sshPort: 22 } },
      zones: [
        { name: 'wan', external: true },
        { name: 'lan', antiLockout: true },
      ],
      interfaces: [],
      rules: [],
    })
    const wrapper = mount(ManagementSection, { global: { stubs: { RouterLink: true } } })
    await flushPromises()
    const button = wrapper.get('button')
    expect(button.text()).toContain('Allow management from wan')
    await button.trigger('click')
    const ids = config.draft.rules.map((r) => r.id)
    expect(ids).toEqual(['web-ui-from-wan', 'ssh-from-wan'])
    expect(config.draft.rules[0]).toMatchObject({
      zone: 'wan',
      action: 'accept',
      protocol: 'tcp',
      destination: { self: true, ports: ['443'] },
    })
    // Once they are there the offer goes away, and the LAN never gets one.
    await flushPromises()
    expect(wrapper.findAll('button').some((b) => b.text().includes('Allow management'))).toBe(false)
  })
})
