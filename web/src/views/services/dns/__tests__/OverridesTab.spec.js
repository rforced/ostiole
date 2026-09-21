import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import OverridesTab from '@/views/services/dns/OverridesTab.vue'

vi.mock('@/lib/api', () => ({
  api: { systemHosts: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  ConfirmButton: true,
  AppDialog: true,
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
}

function draft() {
  return {
    version: 6,
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
        staticLeases: [{ mac: 'aa:bb:cc:00:00:01', ip: '10.0.0.20', hostname: 'calcifer' }],
      },
    },
  }
}

describe('OverridesTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.systemHosts.mockResolvedValue([
      {
        hostname: 'calcifer',
        ip: '10.0.0.20',
        fqdn: 'calcifer.lan',
        mac: 'aa:bb:cc:00:00:01',
        setting: 'dhcp',
      },
    ])
  })

  // A static lease with a hostname is a DNS record nobody wrote on this
  // page. It used to be invisible here, so "why does calcifer resolve"
  // had no answer on the DNS page. Now it is listed, locked, with the
  // name expand-hosts adds and where to change it.
  it("lists the names static leases answer, locked, beside the operator's own", async () => {
    const config = useConfigStore()
    config.draft = draft()
    config.loaded = true
    const wrapper = mount(OverridesTab, { global: { stubs } })
    await flushPromises()
    expect(api.systemHosts).toHaveBeenCalledWith(config.draft)

    // The animated tables are stubbed, so their rows are not under a tbody here.
    const rows = wrapper.findAll('tr').filter((r) => r.find('td').exists())
    const own = rows.find((r) => r.text().includes('printer'))
    expect(own.text()).toContain('10.0.0.30')
    // Every name a row answers is shown in full, so nothing is a guess.
    expect(own.text()).toContain('printer.lan')
    expect(own.text()).toContain('print')
    expect(own.find('button').text()).toBe('Edit')

    const foreign = rows.find((r) => r.text().includes('potato'))
    expect(foreign.text()).toContain('potato.test')

    const lease = rows.find((r) => r.text().includes('calcifer'))
    expect(lease.text()).toContain('calcifer.lan')
    expect(lease.text()).toContain('aa:bb:cc:00:00:01')
    expect(lease.text()).toContain('locked')
    expect(lease.find('button').exists()).toBe(false)
    expect(wrapper.find('a[href="/services/dhcp"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('A name with no domain lives under')
  })

  // Editing the draft re-reads the derived names after a pause, so a
  // lease given a hostname a moment ago shows up here before it is applied.
  it('re-reads the derived names when the draft changes', async () => {
    vi.useFakeTimers()
    try {
      const config = useConfigStore()
      config.draft = draft()
      config.loaded = true
      mount(OverridesTab, { global: { stubs } })
      await flushPromises()
      expect(api.systemHosts).toHaveBeenCalledTimes(1)

      config.draft.services.dhcp.staticLeases[0].hostname = 'howl'
      await flushPromises()
      expect(api.systemHosts).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(300)
      await flushPromises()
      expect(api.systemHosts).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })
})
