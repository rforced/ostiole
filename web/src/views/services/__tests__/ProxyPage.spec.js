import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { h } from 'vue'

import { api } from '@/lib/api'
import { useProxyStatus } from '@/lib/proxyStatus'
import { useConfigStore } from '@/stores/config'
import ProxyPage from '@/views/services/ProxyPage.vue'
import EventsTab from '@/views/services/proxy/EventsTab.vue'
import ProfileDialog from '@/views/services/proxy/ProfileDialog.vue'
import ProxyStatus from '@/views/services/proxy/ProxyStatus.vue'

vi.mock('@/lib/api', () => ({
  api: { proxy: { status: vi.fn(), events: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

// The page takes its tabs from the route; there is no router here.
vi.mock('@/lib/tabs', async () => {
  const { ref } = await import('vue')
  return { usePageTabs: () => ({ tabs: [], tab: ref('service') }) }
})

const stubs = { ConfirmButton: true, RouterLink: true }

function proxy(over = {}) {
  return {
    enabled: true,
    pools: [{ id: 'web', upstreams: [{ address: '192.168.1.20:80' }] }],
    sites: [{ id: 'shop', enabled: true, hosts: ['shop.example.com'], pool: 'web' }],
    ...over,
  }
}

function config(over = {}) {
  return {
    version: 7,
    system: { management: { webPort: 8443, sshPort: 22 } },
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [],
    rules: [],
    services: { proxy: proxy() },
    ...over,
  }
}

function status(over = {}) {
  return {
    setUp: true,
    running: true,
    release: 'v1.2.3',
    ports: { http: 80, https: 443, http3: false },
    upstreams: [],
    ...over,
  }
}

/** Mounts the strip with a saved and a draft configuration of our choosing. */
async function strip(st, { draft = config(), saved = config() } = {}) {
  api.proxy.status.mockResolvedValue(st)
  const store = useConfigStore()
  store.draft = draft
  store.saved = saved
  store.loaded = true
  const wrapper = mount(ProxyStatus, { global: { stubs } })
  await flushPromises()
  return wrapper
}

/** The word in the page header's badge, read the way ProxyPage reads it. */
function badge() {
  return mount({
    setup() {
      const { state } = useProxyStatus()
      return () => h('span', state.value)
    },
  }).text()
}

describe('ProxyStatus', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('names the command when the sidecar is not on the router', async () => {
    const wrapper = await strip(status({ setUp: false, running: false }))
    expect(wrapper.text()).toContain('ostiole repair --proxy')
  })

  it('points at the web UI setting when a listener takes its port', async () => {
    const draft = config({
      system: { management: { webPort: 443, sshPort: 22 } },
      services: { proxy: proxy() },
    })
    const wrapper = await strip(status(), { draft })
    expect(wrapper.text()).toContain('Port 443')
    expect(wrapper.text()).toContain('System, General')
  })

  // Switching it off is a draft edit until it is applied. The router is
  // still serving, and the badge is about the router.
  it('stays running while it is switched off only in the draft', async () => {
    const draft = config({ services: { proxy: { enabled: false } } })
    const wrapper = await strip(status(), { draft })
    expect(badge()).toBe('running')
    expect(wrapper.text()).toContain('v1.2.3')
  })

  it('says nothing beyond the badge when it is off', async () => {
    const off = config({ services: { proxy: { enabled: false } } })
    const wrapper = await strip(status({ running: false }), { draft: off, saved: off })
    expect(badge()).toBe('off')
    expect(wrapper.text()).toBe('')
  })

  it('asks for an apply while the site is only in the draft', async () => {
    const saved = config({ services: { proxy: { enabled: false } } })
    const wrapper = await strip(status({ running: false }), { saved })
    expect(badge()).toBe('off')
    expect(wrapper.text()).toContain('Apply the draft to start it.')
  })

  it('reports the release, the ports and the upstreams when it is running', async () => {
    const wrapper = await strip(
      status({
        ports: { http: 80, https: 443, http3: true },
        upstreams: [
          { address: 'a:80', healthy: true },
          { address: 'b:80', healthy: false },
        ],
      }),
    )
    expect(wrapper.text()).toContain('v1.2.3')
    expect(wrapper.text()).toContain('1 of 2 healthy')
  })

  it('says so when the unit is stopped', async () => {
    const wrapper = await strip(status({ running: false }))
    expect(wrapper.text()).toContain('Stopped.')
  })
})

function event(over = {}) {
  return {
    time: '2026-09-20T14:02:11Z',
    id: 'XmQ1',
    site: 'shop',
    client: '192.168.1.55',
    method: 'GET',
    uri: '/?q=%3Cscript%3E',
    status: 200,
    verdict: 'would-block',
    engine: 'DetectionOnly',
    rules: [{ id: 941100, message: 'XSS Attack Detected', severity: 'critical' }],
    ...over,
  }
}

async function events(list, draft = config()) {
  api.proxy.events.mockResolvedValue(list)
  const store = useConfigStore()
  store.draft = draft
  store.saved = draft
  store.loaded = true
  const wrapper = mount(EventsTab, { global: { stubs } })
  await flushPromises()
  return { wrapper, store }
}

describe('EventsTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('says so when nothing matched in the window', async () => {
    const { wrapper } = await events([])
    expect(wrapper.text()).toContain('Nothing matched in this window.')
  })

  it('badges each verdict', async () => {
    const { wrapper } = await events([
      event({ verdict: 'blocked', status: 403 }),
      event({ id: 'b', verdict: 'would-block' }),
      event({ id: 'c', verdict: 'matched' }),
    ])
    const text = wrapper.text()
    expect(text).toContain('blocked')
    expect(text).toContain('would block')
    expect(text).toContain('matched')
    expect(wrapper.findAll('.badge-bad')).toHaveLength(1)
    expect(wrapper.findAll('.badge-warn')).toHaveLength(1)
  })

  it('shows the path in the row and keeps the query behind a fold', async () => {
    const uri = '/videos/c1323c57/hls1/main/27.mp4?DeviceId=TW96aWxsYS81&ApiKey=ddfabda4c2ec'
    const { wrapper } = await events([event({ uri })])
    const summary = wrapper.find('summary')
    expect(summary.text()).toContain('GET /videos/c1323c57/hls1/main/27.mp4')
    expect(summary.text()).toContain('?…')
    expect(summary.text()).not.toContain('ApiKey')
    expect(summary.attributes('title')).toBe(`GET ${uri}`)
    expect(wrapper.find('details').text()).toContain('ApiKey=ddfabda4c2ec')
  })

  it('shows a rule that matched twice as two chips', async () => {
    const { wrapper } = await events([
      event({
        rules: [
          { id: 942100, message: 'SQL Injection Attack Detected via libinjection' },
          { id: 942100, message: 'SQL Injection Attack Detected via libinjection' },
        ],
      }),
    ])
    expect(wrapper.findAll('.badge.font-mono')).toHaveLength(2)
  })

  it('offers no exclusion when the site has no profile', async () => {
    const { wrapper } = await events([event()])
    expect(wrapper.text()).toContain('The site has no WAF profile.')
    expect(wrapper.text()).not.toContain('Exclude on this path')
  })

  it('adds an exclusion to the draft, on the path when asked', async () => {
    const draft = config({
      services: {
        proxy: proxy({
          wafProfiles: [{ id: 'strict' }],
          sites: [
            { id: 'shop', enabled: true, hosts: ['shop.example.com'], pool: 'web', waf: 'strict' },
          ],
        }),
      },
    })
    const { wrapper, store } = await events([event()], draft)
    const links = wrapper.findAll('button.link')
    await links[0].trigger('click')
    expect(store.proxy.wafProfiles[0].exclusions).toEqual([
      { rule: '941100', description: 'XSS Attack Detected' },
    ])
    await links[1].trigger('click')
    expect(store.proxy.wafProfiles[0].exclusions[1]).toEqual({
      rule: '941100',
      path: '/',
      description: 'XSS Attack Detected',
    })
  })
})

describe('ProfileDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('saves a profile whose exclusion came from the events tab', async () => {
    const profile = {
      id: 'watch',
      exclusions: [{ rule: '941100', description: 'XSS Attack Detected' }],
    }
    const store = useConfigStore()
    store.draft = config({ services: { proxy: proxy({ wafProfiles: [profile] }) } })
    store.loaded = true
    const wrapper = mount(ProfileDialog, {
      props: { open: true, profile },
      global: { stubs: { AppDialog: { template: '<div><slot /></div>' } } },
    })
    await flushPromises()
    await wrapper.find('form').trigger('submit')
    expect(store.proxy.wafProfiles[0].exclusions).toEqual([
      { rule: '941100', description: 'XSS Attack Detected' },
    ])
  })
})

/** Services as a saved configuration has them, with no proxy unless given. */
const withProxy = (p) => config({ services: { dhcp: {}, dns: {}, ...(p && { proxy: p }) } })
const bare = () => withProxy()

function page({ draft = bare(), saved = bare() } = {}) {
  const store = useConfigStore()
  store.draft = draft
  store.saved = saved
  store.loaded = true
  const wrapper = mount(ProxyPage, { global: { stubs: { ...stubs, ProxyStatus: true } } })
  return { wrapper, store }
}

describe('ProxyPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // Opening the page used to write a proxy into the draft, a change nobody
  // made that asked to be applied and stopped signing out.
  it('changes nothing by being opened, and nothing once put back', async () => {
    const { wrapper, store } = page()
    await flushPromises()
    expect(wrapper.find('#proxy-https').exists()).toBe(true)
    expect(store.dirty).toBe(false)

    const on = wrapper.get('input[aria-label="Reverse proxy enabled"]')
    await on.setValue(true)
    expect(store.draft.services.proxy).toEqual({ enabled: true })
    await on.setValue(false)
    expect(store.dirty).toBe(false)

    const https = wrapper.get('#proxy-https')
    await https.setValue('8443')
    expect(store.draft.services.proxy).toEqual({ enabled: false, httpsPort: 8443 })
    await https.setValue('443')
    expect(store.dirty).toBe(false)

    const lan = wrapper.findAll('label').find((l) => l.text() === 'lan')
    await lan.get('input').setValue(true)
    expect(store.draft.services.proxy.zones).toEqual(['lan'])
    await lan.get('input').setValue(false)
    expect(store.dirty).toBe(false)

    await wrapper.get('#proxy-http3').setValue(true)
    expect(store.draft.services.proxy.http3).toBe(true)
    await wrapper.get('#proxy-http3').setValue(false)
    expect(store.dirty).toBe(false)
  })

  // A proxy that is set up is saved with enabled false when off, so off
  // keeps the block and what is in it.
  it('switches a proxy off without losing it', async () => {
    const { wrapper, store } = page({ draft: withProxy(proxy()), saved: withProxy(proxy()) })
    const on = wrapper.get('input[aria-label="Reverse proxy enabled"]')
    await on.setValue(false)
    expect(store.draft.services.proxy).toEqual({ ...proxy(), enabled: false })
    await on.setValue(true)
    expect(store.dirty).toBe(false)
  })
})
