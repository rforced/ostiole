import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import type { ServicesReply, ServiceUnit } from '@/lib/types.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'
import { must, sessionOf } from '@/test/fixtures'
import ServicesPage from '@/views/system/ServicesPage.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class extends Error {},
  api: { systemServices: { list: vi.fn(), restart: vi.fn(), start: vi.fn() } },
}))

const unit = (over: Partial<ServiceUnit>): ServiceUnit => ({
  name: 'DHCP and DNS',
  unit: 'ostiole-dnsmasq.service',
  state: 'active',
  want: true,
  restarts: 0,
  actions: [],
  ...over,
})

function listing(control = true): ServicesReply {
  return {
    control,
    units: [
      unit({
        name: 'Ostiole',
        unit: 'ostiole.service',
        since: '2026-10-29T12:00:00Z',
        actions: control ? ['restart'] : [],
        effect: 'The page reconnects once it is back.',
      }),
      unit({
        since: '2026-10-29T12:00:05Z',
        restarts: 2,
        actions: control ? ['restart'] : [],
        effect: 'DNS stops answering for a few seconds. Leases are kept.',
      }),
      unit({
        name: 'Reverse proxy',
        unit: 'ostiole-proxy.service',
        state: 'inactive',
        failure: 'Exited with an error.',
        actions: control ? ['start'] : [],
      }),
      unit({ name: 'Network', unit: 'systemd-networkd.service' }),
    ],
  }
}

async function open(role: string, reply = listing()) {
  useAuthStore().user = sessionOf({ username: role, role })
  vi.mocked(api.systemServices.list).mockResolvedValue(reply)
  const wrapper = mount(ServicesPage)
  await flushPromises()
  return wrapper
}

const row = (w: VueWrapper, name: string) =>
  must(w.findAll('tbody tr').find((tr) => tr.find('td').text().startsWith(name)))
const button = (tr: DOMWrapper<Element>, label: string) =>
  tr.findAll('button').find((b) => b.text() === label)

describe('ServicesPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })
  afterEach(() => vi.useRealTimers())

  it('shows each unit with its state, start, restarts and what may be done', async () => {
    const w = await open('operator')
    const dns = row(w, 'DHCP and DNS')
    expect(dns.text()).toContain('ostiole-dnsmasq.service')
    expect(dns.text()).toContain('running')
    expect(must(dns.find('td.num')).text()).toBe('2')
    expect(button(dns, 'Restart')).toBeDefined()
    const proxy = row(w, 'Reverse proxy')
    expect(proxy.text()).toContain('stopped')
    expect(proxy.text()).toContain('Exited with an error.')
    expect(button(proxy, 'Start')).toBeDefined()
    expect(button(proxy, 'Restart')).toBeUndefined()
    expect(row(w, 'Network').findAll('button')).toHaveLength(0)
  })

  it('offers a viewer nothing to press', async () => {
    const w = await open('viewer')
    expect(w.findAll('tbody button')).toHaveLength(0)
  })

  it('says why nothing can be restarted when the daemon cannot', async () => {
    const w = await open('admin', listing(false))
    expect(w.findAll('tbody button')).toHaveLength(0)
    expect(w.text()).toContain('cannot restart services')
  })

  it('restarts only once the confirm, which says what drops, is answered', async () => {
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(false)
    const w = await open('operator')
    await must(button(row(w, 'DHCP and DNS'), 'Restart')).trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({
        question: 'Restart DHCP and DNS?',
        description: 'DNS stops answering for a few seconds. Leases are kept.',
      }),
    )
    expect(api.systemServices.restart).not.toHaveBeenCalled()

    ask.mockResolvedValue(true)
    vi.mocked(api.systemServices.restart).mockResolvedValue({ unit: 'ostiole-dnsmasq.service' })
    await must(button(row(w, 'DHCP and DNS'), 'Restart')).trigger('click')
    await flushPromises()
    expect(api.systemServices.restart).toHaveBeenCalledWith('ostiole-dnsmasq.service')
    expect(api.systemServices.list).toHaveBeenCalledTimes(2)
  })

  it('starts without asking, and shows what went wrong', async () => {
    vi.mocked(api.systemServices.start).mockResolvedValue({
      unit: 'ostiole-proxy.service',
      error: 'Job for ostiole-proxy.service failed.',
    })
    const w = await open('operator')
    await must(button(row(w, 'Reverse proxy'), 'Start')).trigger('click')
    await flushPromises()
    expect(api.systemServices.start).toHaveBeenCalledWith('ostiole-proxy.service')
    expect(w.find('[role="alert"]').text()).toBe(
      'Reverse proxy: Job for ostiole-proxy.service failed.',
    )
  })

  it('waits for Ostiole to come back after restarting itself', async () => {
    vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const w = await open('admin')
    vi.useFakeTimers()
    vi.mocked(api.systemServices.restart).mockResolvedValue({
      unit: 'ostiole.service',
      restarting: true,
    })
    const after = listing()
    must(after.units?.[0]).since = '2026-10-29T12:30:00Z'
    vi.mocked(api.systemServices.list)
      .mockRejectedValueOnce(new Error('Failed to fetch'))
      .mockResolvedValueOnce(listing())
      .mockResolvedValue(after)
    await must(button(row(w, 'Ostiole'), 'Restart')).trigger('click')
    await vi.advanceTimersByTimeAsync(1000)
    expect(row(w, 'Ostiole').text()).toContain('Restarting…')
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(row(w, 'Ostiole').text()).not.toContain('Restarting…')
    expect(w.find('[role="alert"]').exists()).toBe(false)
  })
})
