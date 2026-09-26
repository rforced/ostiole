import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import NamesTab from '@/views/services/dns/NamesTab.vue'

vi.mock('@/lib/api', () => ({
  api: { dnsNames: vi.fn(), services: { leases: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  ConfirmButton: true,
  AppDialog: true,
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
}

function draft() {
  return {
    version: 7,
    zones: [{ name: 'lan' }],
    interfaces: [],
    rules: [],
    services: {
      dns: {
        enabled: true,
        domain: 'lan',
        hostOverrides: [
          { hostname: 'printer', ip: '10.0.0.30', aliases: ['print'], description: 'the printer' },
          { hostname: 'potato', domain: 'test', ip: '10.0.0.31' },
        ],
      },
      dhcp: {
        enabled: true,
        servers: [{ interface: 'eth1', enabled: true, dnsRegistration: true }],
        staticLeases: [{ mac: 'aa:bb:cc:00:00:01', ip: '10.0.0.20', hostname: 'calcifer' }],
      },
    },
  }
}

const derived = [
  {
    name: 'calcifer.lan',
    bare: 'calcifer',
    addresses: ['10.0.0.20'],
    source: 'static',
    mac: 'aa:bb:cc:00:00:01',
  },
  {
    name: 'watch.lan',
    bare: 'watch',
    addresses: ['10.0.0.1', '2001:db8:1::1'],
    source: 'proxy',
    site: 'watch',
    interfaces: ['eth1'],
  },
  {
    name: 'Watch.lan',
    bare: 'Watch',
    addresses: ['10.0.0.185'],
    source: 'device',
    mac: '3a:8a:33:e0:8f:75',
    expires: '2026-09-26T21:18:38Z',
    heldBy: { source: 'proxy', name: 'watch' },
  },
]

function rowsOf(wrapper) {
  return wrapper.findAll('tbody tr')
}

describe('NamesTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.dnsNames.mockResolvedValue(derived)
    api.services.leases.mockResolvedValue([])
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  // Every name the router answers is in one list: the operator's own,
  // editable, and the rest locked with where they come from. "Why does
  // watch.lan not reach the proxy" is answered on this page.
  it('lists every name the router answers, with where each comes from', async () => {
    const config = useConfigStore()
    config.draft = draft()
    config.loaded = true
    const wrapper = mount(NamesTab, { global: { stubs } })
    await flushPromises()
    expect(api.dnsNames).toHaveBeenCalledWith(config.draft)

    const rows = rowsOf(wrapper)
    const printer = rows.find((r) => r.text().includes('printer.lan'))
    expect(printer.text()).toContain('10.0.0.30')
    expect(printer.text()).toContain('print')
    expect(printer.text()).toContain('Override')
    expect(printer.find('button').text()).toBe('Edit')
    expect(rows.find((r) => r.text().includes('potato.test')).text()).toContain('10.0.0.31')

    const lease = rows.find((r) => r.text().includes('calcifer.lan'))
    expect(lease.text()).toContain('aa:bb:cc:00:00:01')
    expect(lease.text()).toContain('locked')
    expect(lease.find('button').exists()).toBe(false)
    expect(lease.find('a[href="/services/dhcp#v4"]').text()).toBe('Static lease')

    const site = rows.find(
      (r) => r.text().includes('watch.lan') && r.text().includes('Reverse proxy'),
    )
    expect(site.text()).toContain('2001:db8:1::1')
    expect(site.find('a[href="/services/proxy"]').exists()).toBe(true)

    const device = rows.find((r) => r.text().includes('Watch.lan'))
    expect(device.text()).toContain('3a:8a:33:e0:8f:75')
    expect(device.text()).toContain('Loses the name to reverse proxy site watch.')
    expect(wrapper.text()).toContain('A name with no domain lives under')
  })

  it('finds a name by any part of it', async () => {
    const config = useConfigStore()
    config.draft = draft()
    config.loaded = true
    const wrapper = mount(NamesTab, { global: { stubs } })
    await flushPromises()
    await wrapper.get('input[type="search"]').setValue('watch')
    const shown = rowsOf(wrapper).map((r) => r.text())
    expect(shown).toHaveLength(2)
    expect(shown.some((t) => t.includes('printer'))).toBe(false)
  })

  // Editing the draft re-reads the derived names after a pause, so a
  // lease given a hostname a moment ago shows up here before it is applied.
  it('re-reads the derived names when the draft changes', async () => {
    vi.useFakeTimers()
    const config = useConfigStore()
    config.draft = draft()
    config.loaded = true
    mount(NamesTab, { global: { stubs } })
    await flushPromises()
    expect(api.dnsNames).toHaveBeenCalledTimes(1)

    config.draft.services.dhcp.staticLeases[0].hostname = 'howl'
    await flushPromises()
    expect(api.dnsNames).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(api.dnsNames).toHaveBeenCalledTimes(2)
  })

  // Devices come and go without the draft changing; Live follows them.
  it('keeps reading while Live is on', async () => {
    vi.useFakeTimers()
    const config = useConfigStore()
    config.draft = draft()
    config.loaded = true
    const wrapper = mount(NamesTab, { global: { stubs } })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.dnsNames).toHaveBeenCalledTimes(1)

    await wrapper.get('button[aria-pressed]').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(4000)
    await flushPromises()
    expect(api.dnsNames.mock.calls.length).toBeGreaterThanOrEqual(3)
  })
})
