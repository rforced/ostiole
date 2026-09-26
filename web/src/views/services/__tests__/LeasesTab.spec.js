import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useToastStore } from '@/stores/toast'
import LeasesTab from '@/views/services/dhcp/LeasesTab.vue'

vi.mock('@/lib/api', () => ({
  api: { services: { leases: vi.fn() }, wol: { wake: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  AppDialog: { props: ['open', 'title'], template: '<div v-if="open"><slot /></div>' },
}

const printer = {
  ip: '192.168.1.50',
  mac: 'AA:BB:CC:DD:EE:01',
  hostname: 'printer',
  expires: '2026-09-24T12:00:00Z',
  family: 4,
}

async function tab(leases, { role = 'admin' } = {}) {
  api.services.leases.mockResolvedValue(leases)
  useAuthStore().user = { username: role, role }
  const config = useConfigStore()
  config.replaceDraft({
    version: 7,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true },
      { name: 'eth1', zone: 'lan', enabled: true },
    ],
    services: { dhcp: { enabled: true } },
  })
  config.markSaved()
  const wrapper = mount(LeasesTab, { global: { stubs } })
  await flushPromises()
  return { wrapper, config }
}

const makeStatic = (w) => w.findAll('button').filter((b) => b.text() === 'Make static')
const rows = (w) => w.findAll('tbody tr')
const addresses = (w) => rows(w).map((r) => r.find('td .font-mono').text())
const header = (w, name) => w.findAll('th').find((th) => th.text() === name)

const laptop = {
  ip: '192.168.1.9',
  mac: 'da:a1:19:00:00:02',
  hostname: 'laptop',
  description: 'Work laptop',
  expires: '2026-09-24T18:00:00Z',
  renewed: '2026-09-24T06:00:00Z',
  seen: '2026-09-24T11:00:00Z',
  family: 4,
}

describe('LeasesTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('pins a client to the address it has', async () => {
    const { wrapper, config } = await tab([printer])
    await makeStatic(wrapper)[0].trigger('click')
    expect(wrapper.get('#sl-mac').element.value).toBe('aa:bb:cc:dd:ee:01')
    expect(wrapper.get('#sl-ip').element.value).toBe('192.168.1.50')

    await wrapper.get('form').trigger('submit')
    expect(config.draft.services.dhcp.staticLeases).toEqual([
      { mac: 'aa:bb:cc:dd:ee:01', ip: '192.168.1.50', hostname: 'printer' },
    ])
  })

  // A static lease has nothing to pin, and a DHCPv6 client has no MAC to
  // pin it by.
  it('offers it only where it means something', async () => {
    const { wrapper } = await tab([
      printer,
      { ...printer, mac: 'aa:bb:cc:dd:ee:02', ip: '192.168.1.2', static: true },
      { ip: 'fd00::5', clientId: '00:01:00:01', family: 6, expires: '2026-09-24T12:00:00Z' },
    ])
    expect(makeStatic(wrapper)).toHaveLength(1)
  })

  // The server names the interface a lease came from; a wake goes out
  // there, and only on an inside interface with Ethernet under it.
  it('wakes a client where it got its lease', async () => {
    const { wrapper } = await tab([
      { ...printer, interface: 'eth1' },
      { ...printer, mac: 'aa:bb:cc:dd:ee:02', ip: '203.0.113.9', interface: 'eth0' },
      { ...printer, mac: 'aa:bb:cc:dd:ee:03', ip: '10.9.0.3' },
      { ip: 'fd00::5', clientId: '00:01:00:01', family: 6, expires: '2026-09-24T12:00:00Z' },
    ])
    const wake = wrapper.findAll('button').filter((b) => b.text() === 'Wake')
    expect(wake).toHaveLength(1)
    await wake[0].trigger('click')
    await flushPromises()
    expect(api.wol.wake).toHaveBeenCalledWith({ interface: 'eth1', mac: 'AA:BB:CC:DD:EE:01' })
    expect(useToastStore().toasts.at(-1).message).toBe('Sent a wake packet to printer.')
  })

  it('offers a viewer no wake', async () => {
    const { wrapper } = await tab([{ ...printer, interface: 'eth1' }], { role: 'viewer' })
    expect(wrapper.findAll('button').filter((b) => b.text() === 'Wake')).toHaveLength(0)
  })

  it('searches every field, and a MAC whatever its separators', async () => {
    const v6 = { ip: 'fd00::5', clientId: '00:01:00:01', family: 6, expires: printer.expires }
    const { wrapper } = await tab([printer, laptop, v6])
    const search = wrapper.get('input[type=search]')
    await search.setValue('work')
    expect(addresses(wrapper)).toEqual(['192.168.1.9'])
    expect(wrapper.text()).toContain('1 of 3')
    await search.setValue('aa-bb-cc-dd-ee-01')
    expect(addresses(wrapper)).toEqual(['192.168.1.50'])
    await search.setValue('nobody')
    expect(rows(wrapper)[0].text()).toBe('Nothing matches "nobody".')
  })

  it('sorts by a column header', async () => {
    const { wrapper } = await tab([printer, laptop])
    expect(addresses(wrapper)).toEqual(['192.168.1.9', '192.168.1.50'])
    await header(wrapper, 'Hostname').get('button').trigger('click')
    expect(header(wrapper, 'Hostname').attributes('aria-sort')).toBe('ascending')
    expect(addresses(wrapper)).toEqual(['192.168.1.9', '192.168.1.50'])
    await header(wrapper, 'Hostname').get('button').trigger('click')
    expect(addresses(wrapper)).toEqual(['192.168.1.50', '192.168.1.9'])
  })

  // Pinned, online and random: what the server found, as badges; the
  // times as the router's clock has them.
  it('shows who answered, what is pinned and what never expires', async () => {
    const pinned = {
      ...printer,
      mac: '3c:0a:f3:60:c4:60',
      static: true,
      online: true,
      seen: '2026-09-24T11:59:00Z',
    }
    const forever = { ...printer, ip: '192.168.1.60', mac: 'aa:bb:cc:dd:ee:03', expires: undefined }
    const { wrapper } = await tab([pinned, laptop, forever])
    // By address: the laptop at .9, the printer at .50, then .60.
    const [lap, pin, never] = rows(wrapper).map((r) => r.findAll('td'))
    expect(lap[0].text()).toContain('offline')
    expect(lap[1].text()).toContain('random')
    expect(lap[2].text()).toContain('Work laptop')
    expect(lap[4].text()).toBe(new Date(laptop.seen).toLocaleString())
    expect(lap[5].text()).toBe(new Date(laptop.renewed).toLocaleString())
    expect(pin[0].get('.badge-ok').text()).toBe('online')
    expect(pin[0].text()).toContain('static')
    expect(pin[1].text()).not.toContain('random')
    expect(pin[4].text()).toBe('—')
    expect(pin[5].text()).toBe('—')
    expect(never[6].text()).toBe('never')
  })

  describe('Live', () => {
    beforeEach(() => vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] }))
    afterEach(() => vi.useRealTimers())

    const newer = { ...printer, renewed: '2026-09-24T11:00:00Z' }

    it('reads every two seconds and keeps the newest on top', async () => {
      const { wrapper } = await tab([newer, laptop])
      expect(addresses(wrapper)).toEqual(['192.168.1.9', '192.168.1.50'])
      const live = wrapper.findAll('button').find((b) => b.text() === 'Live')
      await live.trigger('click')
      await flushPromises()
      expect(live.attributes('aria-pressed')).toBe('true')
      expect(api.services.leases).toHaveBeenCalledTimes(2)
      expect(header(wrapper, 'Last renewed').attributes('aria-sort')).toBe('descending')
      expect(addresses(wrapper)).toEqual(['192.168.1.50', '192.168.1.9'])
      expect(header(wrapper, 'Address').get('button').attributes('disabled')).toBeDefined()

      // A lease handed out since lands on top.
      const joined = {
        ...laptop,
        ip: '192.168.1.77',
        mac: 'aa:bb:cc:dd:ee:07',
        renewed: '2026-09-24T11:59:00Z',
      }
      api.services.leases.mockResolvedValue([newer, laptop, joined])
      vi.advanceTimersByTime(2000)
      await flushPromises()
      expect(addresses(wrapper)).toEqual(['192.168.1.77', '192.168.1.50', '192.168.1.9'])

      // Off, the table holds its order and the headers sort again.
      await live.trigger('click')
      vi.advanceTimersByTime(4000)
      await flushPromises()
      expect(api.services.leases).toHaveBeenCalledTimes(3)
      expect(addresses(wrapper)).toEqual(['192.168.1.77', '192.168.1.50', '192.168.1.9'])
      expect(header(wrapper, 'Address').get('button').attributes('disabled')).toBeUndefined()
    })
  })
})
