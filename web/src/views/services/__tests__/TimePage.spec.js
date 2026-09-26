import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useNtpStatus } from '@/lib/ntpStatus'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import TimePage from '@/views/services/TimePage.vue'
import ServerDialog from '@/views/services/time/ServerDialog.vue'

vi.mock('@/lib/api', () => ({
  api: { ntp: { status: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  ConfirmButton: true,
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
}

const DEFAULTS = [
  { host: 'nts.netnod.se', nts: true },
  { host: 'nts.time.nl', nts: true },
  { host: 'a.st1.ntp.br', nts: true },
  { host: 'virginia.time.system76.com', nts: true },
]

function source(name, state, over = {}) {
  return {
    name,
    address: '192.0.2.1',
    state,
    stratum: 2,
    pollSeconds: 64,
    reach: 255,
    lastSeconds: 30,
    offsetSeconds: -0.0045,
    errorSeconds: 0.07,
    nts: 'signed',
    ...over,
  }
}

function status(over = {}) {
  return {
    setUp: true,
    running: true,
    read: true,
    synchronised: true,
    reference: 'nts.time.nl',
    stratum: 2,
    offsetSeconds: -0.0031,
    lastUpdate: new Date(Date.now() - 42000).toISOString(),
    sources: [
      source('nts.netnod.se', 'combined'),
      source('nts.time.nl', 'selected'),
      source('a.st1.ntp.br', 'unusable', { lastSeconds: -1, nts: 'failing' }),
    ],
    served: { packets: 1234, dropped: 0 },
    defaults: DEFAULTS,
    ...over,
  }
}

function config(ntp) {
  return {
    version: 7,
    system: { timezone: 'Europe/Berlin', management: { webPort: 9443, sshPort: 22 } },
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true },
      { name: 'eth1', zone: 'lan', enabled: true },
    ],
    rules: [],
    // Go always writes these three, whatever else the block holds.
    services: {
      dhcp: { enabled: false },
      dns: { enabled: false },
      upnp: { enabled: false },
      ...(ntp ? { ntp: structuredClone(ntp) } : {}),
    },
  }
}

/** Mounts the page on a status and a configuration of our choosing. */
async function page(st, { ntp = undefined, role = 'admin' } = {}) {
  if (st instanceof Promise) api.ntp.status.mockReturnValue(st)
  else api.ntp.status.mockResolvedValue(st)
  useAuthStore().user = { username: role, role }
  const store = useConfigStore()
  store.draft = config(ntp)
  store.saved = config(ntp)
  store.loaded = true
  const wrapper = mount(TimePage, { global: { stubs } })
  await flushPromises()
  return { wrapper, store }
}

/** The row of the servers table that names host. */
function row(wrapper, host) {
  return wrapper.findAll('tbody tr').find((r) => r.text().includes(host))
}

describe('TimePage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    // The read is shared by every page that asks, so each test starts
    // from none.
    useNtpStatus().status.value = null
  })

  it('waits for the first read before saying anything', async () => {
    let answer
    const { wrapper } = await page(new Promise((resolve) => (answer = resolve)))
    expect(wrapper.text()).toContain('Reading…')
    expect(wrapper.find('tbody').text()).toContain('Reading…')
    expect(wrapper.text()).not.toContain('Default')
    // The read is shared; the next test must not wait on this one.
    answer(status())
    await flushPromises()
  })

  it('names the command, and lists the defaults, before the service is set up', async () => {
    const { wrapper } = await page(
      status({ setUp: false, running: false, read: false, sources: [] }),
    )
    expect(wrapper.text()).toContain('ostiole repair')
    expect(wrapper.text()).toContain('the distribution keeps the clock')
    expect(wrapper.text()).not.toContain('Clock')
    expect(wrapper.findAll('tbody tr')).toHaveLength(4)
    expect(wrapper.text()).toContain("These follow Ostiole's defaults until you edit the list.")
    expect(row(wrapper, 'nts.netnod.se').text()).toContain('Signed')
  })

  it('shows what the clock follows and how each server is doing', async () => {
    const { wrapper } = await page(status())
    const text = wrapper.text()
    expect(text).toContain('running')
    expect(text).toContain('synchronised')
    expect(text).toContain('3.1 ms behind')
    expect(text).toContain('Europe/Berlin')
    expect(text).toContain('1,234')
    expect(text).toContain('42s ago')
    expect(row(wrapper, 'nts.time.nl').text()).toContain('in use')
    expect(row(wrapper, 'nts.netnod.se').text()).toContain('agrees')
    expect(row(wrapper, 'nts.netnod.se').text()).toContain('4.5 ms behind')
    // Asked to sign and failing to, and no answer at all.
    const br = row(wrapper, 'a.st1.ntp.br').text()
    expect(br).toContain('failing')
    expect(br).toContain('no answer')
    expect(br).not.toContain('behind')
    // Configured, and not among the sources the service reported.
    expect(row(wrapper, 'virginia.time.system76.com').text()).toContain('no answer')
  })

  it('says the host keeps the clock in a container', async () => {
    const { wrapper } = await page(status({ running: false, read: false, hostClock: true }))
    expect(wrapper.text()).toContain("The host keeps this router's clock.")
    expect(wrapper.text()).not.toContain('Stopped')
    expect(wrapper.text()).not.toContain('stopped')
  })

  it('keeps the defaults when a server is added beside them', async () => {
    const { wrapper, store } = await page(status())
    wrapper.findComponent(ServerDialog).vm.$emit('save', { host: 'gps.lan' }, '')
    await flushPromises()
    const hosts = store.draft.services.ntp.servers.map((s) => s.host)
    expect(hosts).toEqual([...DEFAULTS.map((s) => s.host), 'gps.lan'])
    expect(row(wrapper, 'gps.lan').text()).toContain('Unsigned')
    expect(wrapper.text()).not.toContain("These follow Ostiole's defaults")
  })

  it('goes back to the defaults, and the block goes with its last field', async () => {
    const { wrapper, store } = await page(status(), { ntp: { servers: [{ host: 'gps.lan' }] } })
    const button = wrapper.findAll('button').find((b) => b.text() === 'Use defaults')
    await button.trigger('click')
    expect(store.draft.services.ntp).toBeUndefined()
    expect(wrapper.findAll('tbody tr')).toHaveLength(4)
  })

  it('switches serving off without leaving an empty block behind', async () => {
    const { wrapper, store } = await page(status(), { ntp: { serve: true } })
    const serve = wrapper.get('#ntp-serve')
    expect(serve.element.checked).toBe(true)
    await serve.setValue(false)
    expect(store.draft.services.ntp).toBeUndefined()
    await serve.setValue(true)
    expect(store.draft.services.ntp).toEqual({ serve: true })
    expect(store.dirty).toBe(false)
  })

  it('lets a viewer read and change nothing', async () => {
    const { wrapper } = await page(status(), { role: 'viewer' })
    expect(wrapper.text()).not.toContain('Add server')
    expect(row(wrapper, 'nts.time.nl').text()).toContain('View')
    expect(wrapper.get('#ntp-serve').element.matches(':disabled')).toBe(true)
  })
})

describe('ServerDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function dialog(props = {}) {
    return mount(ServerDialog, { props: { open: true, ...props }, global: { stubs } })
  }

  it('refuses signed answers from an address', async () => {
    const wrapper = dialog()
    await wrapper.get('#ntp-host').setValue('192.168.1.5')
    expect(wrapper.get('[role=alert]').text()).toContain('Signed answers need a name')
    expect(wrapper.get('button[type=submit]').element.disabled).toBe(true)
  })

  it('refuses a server listed already', async () => {
    const wrapper = dialog({ hosts: ['nts.netnod.se'] })
    await wrapper.get('#ntp-host').setValue('NTS.netnod.se')
    expect(wrapper.get('[role=alert]').text()).toContain('listed already')
  })

  it('keeps an unsigned server unsigned, and saves it under its old name', async () => {
    const wrapper = dialog({ server: { host: 'gps.lan' }, hosts: ['gps.lan'] })
    await wrapper.get('#ntp-host').setValue('gps2.lan')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')[0]).toEqual([{ host: 'gps2.lan' }, 'gps.lan'])
  })
})
