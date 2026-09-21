import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import ResolverTab from '@/views/services/dns/ResolverTab.vue'

vi.mock('@/lib/api', () => ({
  api: { clearDnsCache: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

function draft() {
  return {
    version: 6,
    zones: [
      { name: 'lan', description: 'Home' },
      { name: 'wan', external: true },
    ],
    interfaces: [
      { name: 'enp2s0', zone: 'lan', enabled: true, description: 'Office' },
      { name: 'wlp3s0', zone: 'lan', enabled: false },
      { name: 'br0', zone: 'lan', enabled: true },
      { name: 'enp1s0', zone: 'wan', enabled: true, description: 'Cable' },
    ],
    rules: [],
    services: { dns: { enabled: true } },
  }
}

/** The fields most routers leave alone sit behind the Advanced fold. */
async function openAdvanced(wrapper) {
  await wrapper
    .findAll('button')
    .find((b) => b.text().includes('Advanced'))
    .trigger('click')
  await flushPromises()
}

describe('ResolverTab', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // "Every interface outside external zones" used to be a box with no
  // list under it: the operator could not see what "every" came to before
  // opting out of it. The list is spelled out, by the names people use.
  it('shows what listening everywhere comes to, description first', async () => {
    const config = useConfigStore()
    config.draft = draft()
    config.loaded = true
    const wrapper = mount(ResolverTab)
    await flushPromises()
    await openAdvanced(wrapper)

    const listed = wrapper.findAll('[aria-label="Listening on"] li').map((li) => li.text())
    expect(listed).toHaveLength(2)
    expect(listed[0]).toContain('Office')
    expect(listed[0]).toContain('enp2s0')
    expect(listed[1]).toContain('br0')
    // The WAN is external and the disabled radio is not listening.
    expect(listed.join(' ')).not.toContain('Cable')
    expect(listed.join(' ')).not.toContain('wlp3s0')

    // Opting out lists the same interfaces as choices, plus the WAN.
    const everywhere = wrapper.findAll('label').find((l) => l.text().includes('Every interface'))
    await wrapper.find(`[id="${everywhere.attributes('for')}"]`).setValue(false)
    const choices = wrapper.findAll('label').filter((l) => l.find('input[type=checkbox]').exists())
    expect(choices.map((l) => l.text()).join(' ')).toContain('Cable')
  })

  // The tab is a draft editor, so an action that reached for the config
  // would throw away whatever was being typed. It names what it cleared.
  it('clears the cache without touching the draft', async () => {
    const config = useConfigStore()
    config.draft = draft()
    config.saved = draft()
    config.loaded = true
    api.clearDnsCache.mockResolvedValue({ cleared: ['dnsmasq', 'unbound'] })

    const wrapper = mount(ResolverTab)
    await openAdvanced(wrapper)
    config.draft.services.dns.domain = 'unsaved'
    const button = wrapper.findAll('button').find((b) => b.text().includes('Clear cache'))
    await button.trigger('click')
    await flushPromises()

    expect(api.clearDnsCache).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('Cleared the DNS server and the validating resolver.')
    expect(config.draft.services.dns.domain).toBe('unsaved')
  })

  // Nothing is caching, so there is nothing the button could do.
  it('disables the button when the saved configuration has DNS off', async () => {
    const config = useConfigStore()
    config.draft = draft()
    config.saved = { ...draft(), services: { dns: { enabled: false } } }
    config.loaded = true

    const wrapper = mount(ResolverTab)
    await flushPromises()
    await openAdvanced(wrapper)
    const button = wrapper.findAll('button').find((b) => b.text().includes('Clear cache'))
    expect(button.attributes('disabled')).toBeDefined()
  })
})
