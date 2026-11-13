import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import type { ProxySite } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'
import { configOf, draftOf } from '@/test/fixtures'
import SiteDialog from '@/views/services/proxy/SiteDialog.vue'

const site = {
  id: 'shop',
  enabled: true,
  hosts: ['shop.example.test'],
  pool: 'web',
  allowFrom: ['office', '192.0.2.0/24'],
}

function open(sites: ProxySite[] = [site]) {
  const cfg = configOf({
    version: 11,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [],
    rules: [],
    aliases: [{ name: 'office', type: 'hosts', entries: ['198.51.100.0/24'] }],
    services: {
      dhcp: { enabled: false },
      dns: { enabled: false },
      proxy: {
        enabled: true,
        pools: [{ id: 'web', upstreams: [{ address: '192.168.1.20:80' }] }],
        sites: structuredClone(sites),
      },
    },
  })
  const config = useConfigStore()
  config.saved = structuredClone(cfg)
  config.replaceDraft(cfg)
  config.loaded = true
  const wrapper = mount(SiteDialog, {
    props: { open: true, site: draftOf(config).services?.proxy?.sites?.[0] },
    global: { stubs: { AppDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
  })
  return { wrapper, config }
}

describe('SiteDialog untouched save', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps the order of the allow list', async () => {
    const { wrapper, config } = open()
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('#site-allow').element.value).toBe('192.0.2.0/24')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('writes an allow list the user changes', async () => {
    const { wrapper, config } = open()
    await flushPromises()
    await wrapper.get('#site-allow').setValue('192.0.2.0/24\n203.0.113.0/24')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(true)
    expect(draftOf(config).services?.proxy?.sites?.[0]?.allowFrom).toEqual([
      '192.0.2.0/24',
      '203.0.113.0/24',
      'office',
    ])
  })

  it('shows and keeps the hidden prefixes in their saved order', async () => {
    const { wrapper, config } = open([{ ...site, redactPaths: ['/s/', '/feed/'] }])
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('#site-redact').element.value).toBe('/s/\n/feed/')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('adds no redactPaths key to a site with no prefixes', async () => {
    const { wrapper, config } = open()
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('#site-redact').element.value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
    expect(draftOf(config).services?.proxy?.sites?.[0]).not.toHaveProperty('redactPaths')
  })

  it('writes the prefixes the user types and drops the key once they are cleared', async () => {
    const { wrapper, config } = open()
    await flushPromises()
    await wrapper.get('#site-redact').setValue('/s/\n/feed/')
    await wrapper.get('form').trigger('submit')
    expect(draftOf(config).services?.proxy?.sites?.[0]?.redactPaths).toEqual(['/s/', '/feed/'])
    await wrapper.setProps({ site: draftOf(config).services?.proxy?.sites?.[0] })
    await wrapper.get('#site-redact').setValue('')
    await wrapper.get('form').trigger('submit')
    expect(draftOf(config).services?.proxy?.sites?.[0]).not.toHaveProperty('redactPaths')
    expect(config.dirty).toBe(false)
  })

  it('says what the router would refuse and holds Save back', async () => {
    const { wrapper } = open()
    await flushPromises()
    const save = () => wrapper.get<HTMLButtonElement>('button[type="submit"]').element.disabled
    expect(save()).toBe(false)
    await wrapper.get('#site-redact').setValue('feed')
    expect(wrapper.get('[role="alert"]').text()).toBe(
      '"feed" must start and end with / and hold at least 3 characters',
    )
    expect(save()).toBe(true)
    await wrapper.get('#site-redact').setValue('/feed/')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(save()).toBe(false)
  })

  it('counts the prefixes of the other sites, each once', async () => {
    const many = Array.from({ length: 10 }, (_, i) => `/p${i}/`)
    const { wrapper } = open([site, { ...site, id: 'blog', redactPaths: many }])
    await flushPromises()
    await wrapper.get('#site-redact').setValue('/p0/')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.get('#site-redact').setValue('/p0/\n/more/')
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'the proxy hides at most 10 different prefixes across its sites',
    )
  })

  it('lets a list left as saved through when the proxy is already over the cap', async () => {
    const many = Array.from({ length: 11 }, (_, i) => `/p${i}/`)
    const { wrapper, config } = open([{ ...site, redactPaths: many }])
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})
