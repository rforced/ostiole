import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useToastStore } from '@/stores/toast'
import WolPage from '@/views/services/WolPage.vue'

vi.mock('@/lib/api', () => ({
  api: { wol: { wake: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  ConfirmButton: true,
  AppDialog: { props: ['open', 'title'], template: '<div v-if="open"><slot /></div>' },
}

function config() {
  return {
    version: 7,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true },
      { name: 'eth1', zone: 'lan', enabled: true, description: 'Office' },
      { name: 'eth2', zone: 'lan', enabled: false },
      { name: 'wg0', zone: 'lan', enabled: true, wireguard: {} },
    ],
    rules: [],
    services: {
      dhcp: { enabled: false },
      dns: { enabled: false },
      upnp: { enabled: false },
      wol: {
        devices: [
          { id: 'wol-nas', interface: 'eth1', mac: 'aa:bb:cc:00:00:01', description: 'NAS' },
          { id: 'wol-pc', interface: 'eth1', mac: 'aa:bb:cc:00:00:02' },
          { id: 'wol-lab', interface: 'eth2', mac: 'aa:bb:cc:00:00:03', description: 'Lab' },
        ],
      },
    },
  }
}

async function page(role = 'admin') {
  useAuthStore().user = { username: role, role }
  const store = useConfigStore()
  store.draft = config()
  store.saved = config()
  store.loaded = true
  const wrapper = mount(WolPage, { global: { stubs } })
  await flushPromises()
  return { wrapper, store }
}

const row = (w, text) => w.findAll('tbody tr').find((tr) => tr.text().includes(text))
const button = (el, label) => el.findAll('button').find((b) => b.text() === label)

describe('WolPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('lists the devices, and holds back one whose interface is off', async () => {
    const { wrapper } = await page()
    expect(wrapper.findAll('tbody tr')).toHaveLength(3)
    expect(row(wrapper, 'NAS').text()).toContain('aa:bb:cc:00:00:01')
    // No description: the MAC names it.
    expect(row(wrapper, 'aa:bb:cc:00:00:02').find('td').text()).toBe('aa:bb:cc:00:00:02')
    const lab = row(wrapper, 'Lab')
    expect(lab.text()).toContain('interface off')
    expect(button(lab, 'Wake').attributes('disabled')).toBeDefined()
    expect(button(row(wrapper, 'NAS'), 'Wake').attributes('disabled')).toBeUndefined()
  })

  it('wakes a device and says it was sent, or why not', async () => {
    const { wrapper } = await page('operator')
    const toast = useToastStore()
    api.wol.wake.mockResolvedValueOnce(undefined)
    await button(row(wrapper, 'NAS'), 'Wake').trigger('click')
    await flushPromises()
    expect(api.wol.wake).toHaveBeenCalledWith({ interface: 'eth1', mac: 'aa:bb:cc:00:00:01' })
    expect(toast.toasts.at(-1).message).toBe('Sent a wake packet to NAS.')

    api.wol.wake.mockRejectedValueOnce(new Error('sending a wake needs the daemon to run as root'))
    await button(row(wrapper, 'NAS'), 'Wake').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'NAS: sending a wake needs the daemon to run as root',
    )
  })

  it('wakes all but the device whose interface is off', async () => {
    const { wrapper } = await page()
    api.wol.wake.mockResolvedValue(undefined)
    await button(wrapper, 'Wake all').trigger('click')
    await flushPromises()
    expect(api.wol.wake.mock.calls.map(([b]) => b.mac)).toEqual([
      'aa:bb:cc:00:00:01',
      'aa:bb:cc:00:00:02',
    ])
    expect(useToastStore().toasts.at(-1).message).toBe('Sent wake packets to 2 devices.')
  })

  it('shows a viewer the list and nothing that acts', async () => {
    const { wrapper } = await page('viewer')
    expect(wrapper.findAll('tbody tr')).toHaveLength(3)
    for (const label of ['Wake', 'Wake all', 'Add device', 'Edit'])
      expect(button(wrapper, label)).toBeUndefined()
    expect(button(row(wrapper, 'NAS'), 'View')).toBeTruthy()
    expect(wrapper.text()).not.toContain('Wake a device')
  })

  it('adds a device to the draft, written the way the server wants it', async () => {
    const { wrapper, store } = await page()
    await button(wrapper, 'Add device').trigger('click')
    // Inside interfaces with Ethernet under them, the one that is off
    // included, and nothing facing the internet or tunnelled.
    const options = wrapper.findAll('#wol-if option').map((o) => o.element.value)
    expect(options).toEqual(['eth1', 'eth2'])
    await wrapper.get('#wol-desc').setValue(' Desktop ')
    await wrapper.get('#wol-mac').setValue('AA-BB-CC-00-00-04')
    await wrapper.get('#wol-if').setValue('eth2')
    await wrapper.get('form').trigger('submit')
    const added = store.wolDevices.at(-1)
    expect(added).toMatchObject({
      interface: 'eth2',
      mac: 'aa:bb:cc:00:00:04',
      description: 'Desktop',
    })
    expect(added.id).toMatch(/^wol-[a-z0-9]{6}$/)
  })

  it('refuses an address that names no one machine, or one already listed', async () => {
    const { wrapper, store } = await page()
    await button(wrapper, 'Add device').trigger('click')
    for (const [mac, want] of [
      ['aa:bb:cc', 'A MAC address looks like aa:bb:cc:dd:ee:ff.'],
      ['01:00:5e:00:00:fb', '01:00:5e:00:00:fb is a group address'],
      ['AA:BB:CC:00:00:01', 'aa:bb:cc:00:00:01 is already listed.'],
    ]) {
      await wrapper.get('#wol-mac').setValue(mac)
      await wrapper.get('form').trigger('submit')
      expect(wrapper.get('[role="alert"]').text()).toContain(want)
    }
    expect(store.wolDevices).toHaveLength(3)
  })

  it('wakes a machine that is not listed, on an interface the router is running', async () => {
    const { wrapper } = await page()
    // Only what the saved configuration can send on.
    expect(wrapper.findAll('#wol-once-if option').map((o) => o.element.value)).toEqual(['eth1'])
    api.wol.wake.mockResolvedValueOnce(undefined)
    await wrapper.get('#wol-once-mac').setValue('AA-BB-CC-00-00-09')
    await wrapper.findAll('form').at(-1).trigger('submit')
    await flushPromises()
    expect(api.wol.wake).toHaveBeenCalledWith({ interface: 'eth1', mac: 'aa:bb:cc:00:00:09' })
    expect(useToastStore().toasts.at(-1).message).toBe(
      'Sent a wake packet to aa:bb:cc:00:00:09 on eth1.',
    )
  })
})
