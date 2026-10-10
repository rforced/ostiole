import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import DiscoveryPage from '@/views/services/DiscoveryPage.vue'

vi.mock('@/lib/api', () => ({
  api: {
    services: {
      status: vi.fn(),
      discoveryLog: vi.fn(),
      clearDiscoveryLog: vi.fn(),
      discoveryAnnouncements: vi.fn(),
    },
    systemStats: vi.fn(),
    logFiles: vi.fn(),
    logLimits: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}))

// The page takes its tabs from the route; there is no router here.
vi.mock('@/lib/tabs', async () => {
  const { ref } = await import('vue')
  return {
    usePageTabs: () => ({
      tabs: [
        { value: 'settings', label: 'Settings' },
        { value: 'log', label: 'Log' },
        { value: 'announcements', label: 'Announcements' },
      ],
      tab: ref('settings'),
    }),
  }
})

const stubs = { ConfirmButton: true, RouterLink: { template: '<a><slot /></a>' } }

function config(discovery) {
  return {
    version: 11,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }, { name: 'iot' }],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true },
      { name: 'eth1', zone: 'lan', enabled: true, description: 'Office' },
      { name: 'eth2', zone: 'iot', enabled: true },
      { name: 'eth3', zone: 'lan', enabled: false },
      { name: 'wg0', zone: 'lan', enabled: true, wireguard: {} },
    ],
    rules: [],
    services: {
      dhcp: { enabled: false },
      dns: { enabled: false },
      ...(discovery ? { discovery } : {}),
    },
  }
}

const relay = () => ({
  enabled: true,
  mdns: true,
  ssdp: false,
  interfaces: [
    { interface: 'eth1', asks: true, answers: true },
    { interface: 'eth2', asks: false, answers: true },
  ],
  services: ['_googlecast._tcp', '_airplay._tcp'],
  log: { entries: 5000 },
})

async function page(discovery, role = 'admin') {
  useAuthStore().user = { username: role, role }
  const store = useConfigStore()
  store.saved = config(discovery)
  store.draft = config(discovery)
  store.loaded = true
  const wrapper = mount(DiscoveryPage, { global: { stubs } })
  await flushPromises()
  return { wrapper, store }
}

const row = (w, name) => w.findAll('tbody tr').find((tr) => tr.text().includes(name))
const box = (w, name, role) => w.get(`input[aria-label="${name} ${role}"]`)
const toggle = (w, label) => {
  const l = w.findAll('label').find((x) => x.text() === label)
  return w.get(`input[id="${l.attributes('for')}"]`)
}

describe('DiscoveryPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.services.status.mockResolvedValue({ discoveryRunning: true })
  })

  it.each([
    ['without a relay block', undefined],
    ['with one', relay()],
  ])('leaves the draft as it was when opened %s', async (_, discovery) => {
    const { wrapper, store } = await page(discovery)
    const before = JSON.stringify(store.saved)
    expect(box(wrapper, 'eth1', 'asks').element.checked).toBe(Boolean(discovery))
    expect(JSON.stringify(store.draft)).toBe(before)
    expect(store.dirty).toBe(false)
  })

  it('adds a network in both roles at the first tick', async () => {
    const { wrapper, store } = await page(undefined, 'operator')
    await box(wrapper, 'eth2', 'asks').setValue(true)
    expect(store.draft.services.discovery).toEqual({
      enabled: false,
      mdns: true,
      ssdp: true,
      interfaces: [{ interface: 'eth2', asks: true, answers: true }],
    })
    expect(box(wrapper, 'eth2', 'answers').element.checked).toBe(true)
  })

  it('takes a network out when both roles are cleared', async () => {
    const { wrapper, store } = await page(relay())
    await box(wrapper, 'eth2', 'answers').setValue(false)
    expect(store.draft.services.discovery.interfaces).toEqual([
      { interface: 'eth1', asks: true, answers: true },
    ])
    await box(wrapper, 'eth1', 'asks').setValue(false)
    await box(wrapper, 'eth1', 'answers').setValue(false)
    expect(store.draft.services.discovery.interfaces).toBeUndefined()
  })

  it('drops the block once it is back to what a router without one reads as', async () => {
    const { wrapper, store } = await page(undefined)
    await box(wrapper, 'eth1', 'asks').setValue(true)
    await box(wrapper, 'eth1', 'asks').setValue(false)
    await box(wrapper, 'eth1', 'answers').setValue(false)
    expect(store.draft.services.discovery).toBeUndefined()
    expect(store.dirty).toBe(false)
  })

  it('keeps the protocols as ticked once a block is saved, defaults included', async () => {
    const { wrapper, store } = await page({ enabled: false, mdns: true, ssdp: false })
    await toggle(wrapper, 'mDNS').setValue(false)
    expect(store.draft.services.discovery).toEqual({ enabled: false, mdns: false, ssdp: false })
    await toggle(wrapper, 'mDNS').setValue(true)
    await toggle(wrapper, 'SSDP').setValue(true)
    expect(store.draft.services.discovery).toEqual({ enabled: false, mdns: true, ssdp: true })
    await toggle(wrapper, 'SSDP').setValue(false)
    expect(store.dirty).toBe(false)
  })

  it('reads a protocol the block leaves out as on, as the server does', async () => {
    const { wrapper } = await page({ enabled: false, ssdp: false })
    expect(toggle(wrapper, 'mDNS').element.checked).toBe(true)
    expect(toggle(wrapper, 'SSDP').element.checked).toBe(false)
  })

  it('offers the inside networks a multicast reaches, off ones included', async () => {
    const { wrapper } = await page(undefined)
    expect(row(wrapper, 'eth3')).toBeTruthy()
    expect(row(wrapper, 'wg0')).toBeUndefined()
    expect(row(wrapper, 'eth0')).toBeUndefined()
  })

  it('writes the service types one per line, trimmed', async () => {
    const { wrapper, store } = await page(relay())
    const area = wrapper.get('textarea')
    expect(area.element.value).toBe('_googlecast._tcp\n_airplay._tcp')
    await area.setValue('  _googlecast._tcp \n\n_spotify-connect._tcp\n')
    expect(store.draft.services.discovery.services).toEqual([
      '_googlecast._tcp',
      '_spotify-connect._tcp',
    ])
    // A new line being typed stays in the box.
    expect(area.element.value).toBe('  _googlecast._tcp \n\n_spotify-connect._tcp\n')
    await area.setValue('')
    expect(store.draft.services.discovery.services).toBeUndefined()
  })

  it('shows a viewer the settings locked', async () => {
    const { wrapper } = await page(relay(), 'viewer')
    expect(
      wrapper.get('input[aria-label="Discovery enabled"]').attributes('disabled'),
    ).toBeDefined()
    expect(box(wrapper, 'eth1', 'asks').element.closest('fieldset[disabled]')).toBeTruthy()
    expect(wrapper.get('textarea').element.closest('fieldset[disabled]')).toBeTruthy()
  })
})

describe('deleting an interface in the relay', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('says so and takes it out of the relay', () => {
    const store = useConfigStore()
    store.saved = config(relay())
    store.draft = config(relay())
    store.loaded = true
    expect(store.interfaceDependents('eth2')).toContain('discovery relay on eth2')
    store.removeInterface('eth2')
    expect(store.draft.services.discovery.interfaces.map((l) => l.interface)).toEqual(['eth1'])
  })
})
