import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import NetworkDialog from '@/views/wireless/NetworkDialog.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const mod = await importOriginal()
  return { ...mod, api: { ...mod.api, wireless: { radios: vi.fn() } } }
})

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

const guest = {
  name: 'ap0',
  zone: 'lan',
  enabled: true,
  ipv4: { mode: 'none' },
  ipv6: { mode: 'none' },
  wireless: { radio: 'wlp3s0', ssid: 'guests', security: 'owe' },
}

async function open(radios, network = null) {
  api.wireless.radios.mockResolvedValue(radios)
  const config = useConfigStore()
  config.replaceDraft({
    version: 11,
    zones: [{ name: 'lan' }],
    interfaces: [{ name: 'eth1', zone: 'lan', enabled: true }, ...(network ? [network] : [])],
    rules: [],
    wireless: { country: 'US', radios: [{ name: 'wlp3s0', enabled: true, band: '5g' }] },
  })
  const wrapper = mount(NetworkDialog, {
    props: { open: true, network: network && config.findInterface(network.name) },
    global: { stubs },
  })
  await flushPromises()
  return wrapper
}

const owe = (w) => w.get('#net-security').find('option[value="owe"]')

describe('NetworkDialog Enhanced open', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('is offered where hostapd has it', async () => {
    const w = await open({ setUp: true, enhancedOpen: true, radios: [] })
    expect(owe(w).attributes('disabled')).toBeUndefined()
    expect(owe(w).text()).toBe('Enhanced open')
  })

  // Arch builds hostapd without it, and the apply would refuse it.
  it('is greyed out, and says why, where hostapd lacks it', async () => {
    const w = await open({ setUp: true, enhancedOpen: false, radios: [] })
    expect(owe(w).attributes('disabled')).toBeDefined()
    expect(owe(w).text()).toBe('Enhanced open, not on this router')
  })

  it('keeps a network already on it, and tells it', async () => {
    const w = await open({ setUp: true, enhancedOpen: false, radios: [] }, guest)
    expect(w.get('#net-security').element.value).toBe('owe')
    expect(owe(w).attributes('disabled')).toBeUndefined()
    expect(w.text()).toContain('Not on this router: its wireless service was built without it.')
  })

  it('says nothing of it where hostapd is not set up', async () => {
    const w = await open({ setUp: false, radios: [] })
    expect(owe(w).attributes('disabled')).toBeUndefined()
    expect(owe(w).text()).toBe('Enhanced open')
  })
})
