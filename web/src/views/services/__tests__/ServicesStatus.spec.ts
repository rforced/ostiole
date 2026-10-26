import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import type { Config } from '@/lib/model.gen'
import type { ServiceName } from '@/lib/servicesStatus'
import { useConfigStore } from '@/stores/config'
import ServicesStatus from '@/views/services/ServicesStatus.vue'

vi.mock('@/lib/api', () => ({
  api: { services: { status: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

function draft(resolver = 'tls') {
  return {
    version: 11,
    zones: [],
    interfaces: [],
    rules: [],
    services: { dns: { enabled: true, resolver }, dhcp: { enabled: false } },
  } as Config
}

async function strip(status, service: ServiceName = 'dns') {
  vi.mocked(api.services.status).mockResolvedValue(status)
  const store = useConfigStore()
  store.draft = draft()
  store.saved = draft()
  store.loaded = true
  const wrapper = mount(ServicesStatus, { props: { service } })
  await flushPromises()
  return wrapper
}

describe('ServicesStatus', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // Behind a network that blocks port 853, DNS over TLS fails with the
  // resolver running and nothing in its state to say why. The strip names
  // the upstreams that do not answer and what would still resolve.
  it('names the DNS over TLS upstreams that do not answer', async () => {
    const wrapper = await strip({
      setUp: true,
      running: true,
      resolverSetUp: true,
      resolverRunning: true,
      resolverUnreachable: ['1.1.1.1', '9.9.9.9'],
      resolverNoneAnswer: true,
    })
    const note = wrapper.find('[role=note]')
    expect(note.text()).toContain('No answer on port 853 from 1.1.1.1, 9.9.9.9')
    expect(note.text()).toContain('Validate or Forward')
  })

  it('says names still resolve while another upstream answers', async () => {
    const wrapper = await strip({
      setUp: true,
      running: true,
      resolverSetUp: true,
      resolverRunning: true,
      resolverUnreachable: ['9.9.9.9'],
    })
    const note = wrapper.find('[role=note]')
    expect(note.text()).toContain(
      'No answer on port 853 from 9.9.9.9. Names resolve over the others.',
    )
    expect(note.text()).not.toContain('every name fails')
  })

  it('says nothing about port 853 when every upstream answers', async () => {
    const wrapper = await strip({
      setUp: true,
      running: true,
      resolverSetUp: true,
      resolverRunning: true,
    })
    expect(wrapper.find('[role=note]').exists()).toBe(false)
  })
})
