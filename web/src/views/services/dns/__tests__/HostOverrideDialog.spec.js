import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import HostOverrideDialog from '@/views/services/dns/HostOverrideDialog.vue'

vi.mock('@/lib/api', () => ({
  api: { services: { leases: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  AppDialog: { props: ['open', 'title'], template: '<div><slot /></div>' },
}

async function open(override = null) {
  useConfigStore().replaceDraft({
    version: 6,
    zones: [{ name: 'lan' }],
    interfaces: [],
    rules: [],
    services: {
      dns: { enabled: true, domain: 'jd', hostOverrides: override ? [override] : [] },
      dhcp: {
        enabled: true,
        staticLeases: [{ mac: 'aa:bb:cc:00:00:05', ip: '10.0.0.5', hostname: 'switch' }],
      },
    },
  })
  const w = mount(HostOverrideDialog, { props: { open: false, override }, global: { stubs } })
  await w.setProps({ open: true })
  await flushPromises()
  return w
}

// A device that asks for a name an override holds keeps its address and
// loses the name, and the only trace of it was a warning in the DHCP log.
describe('HostOverrideDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.services.leases.mockResolvedValue([
      { mac: 'd2:4f:86:8b:6c:5c', ip: '10.0.0.249', hostname: 'Watch', family: 4 },
      { mac: 'aa:bb:cc:00:00:05', ip: '10.0.0.5', hostname: 'switch', family: 4 },
    ])
  })

  it('warns that a device asks for the name', async () => {
    const w = await open({ hostname: 'watch', ip: '10.0.0.1' })
    expect(w.text()).toContain('A device at 10.0.0.249 asks for Watch as well.')
    expect(w.get('#ho-name').attributes('aria-describedby')).toBe('ho-taken')
  })

  it('warns about an alias, and about a name typed under the local domain', async () => {
    const w = await open()
    await w.get('#ho-name').setValue('proxy')
    await w.get('#ho-ip').setValue('10.0.0.1')
    expect(w.find('#ho-taken').exists()).toBe(false)
    await w.get('#ho-aliases').setValue('tv, watch')
    expect(w.get('#ho-taken').text()).toContain('10.0.0.249')
    await w.get('#ho-aliases').setValue('')
    await w.get('#ho-domain').setValue('JD.')
    await w.get('#ho-name').setValue('watch')
    expect(w.get('#ho-taken').text()).toContain('10.0.0.249')
  })

  it('stays quiet where nothing is lost', async () => {
    // The device's own address.
    let w = await open({ hostname: 'watch', ip: '10.0.0.249' })
    expect(w.find('#ho-taken').exists()).toBe(false)
    // Another domain, which the device's name never reaches.
    w = await open({ hostname: 'watch', domain: 'example', ip: '10.0.0.1' })
    expect(w.find('#ho-taken').exists()).toBe(false)
    // Another family.
    w = await open({ hostname: 'watch', ip: '2001:db8::1' })
    expect(w.find('#ho-taken').exists()).toBe(false)
    // A static lease that names its device is refused on apply instead.
    w = await open({ hostname: 'switch', ip: '10.0.0.1' })
    expect(w.find('#ho-taken').exists()).toBe(false)
  })

  it('opens without the leases', async () => {
    api.services.leases.mockRejectedValue(new Error('no'))
    const w = await open({ hostname: 'watch', ip: '10.0.0.1' })
    expect(w.get('#ho-name').element.value).toBe('watch')
    expect(w.find('#ho-taken').exists()).toBe(false)
  })
})
