import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { h } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import { api } from '@/lib/api'
import { eventValues } from '@/lib/proxyEvents'
import { matches } from '@/lib/search'
import { useProxyStatus } from '@/lib/proxyStatus'
import { useConfigStore } from '@/stores/config'
import ProxyPage from '@/views/services/ProxyPage.vue'
import AccessDialog from '@/views/services/proxy/AccessDialog.vue'
import EventsTab from '@/views/services/proxy/EventsTab.vue'
import ExclusionDialog from '@/views/services/proxy/ExclusionDialog.vue'
import ProfileDialog from '@/views/services/proxy/ProfileDialog.vue'
import ProxyStatus from '@/views/services/proxy/ProxyStatus.vue'
import RouteDialog from '@/views/services/proxy/RouteDialog.vue'
import RoutesTab from '@/views/services/proxy/RoutesTab.vue'
import ServiceTab from '@/views/services/proxy/ServiceTab.vue'

vi.mock('@/lib/api', () => ({
  api: {
    proxy: { status: vi.fn(), events: vi.fn(), clearEvents: vi.fn() },
    systemStats: vi.fn(),
    logFiles: vi.fn(),
    logLimits: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}))

/** The stream the events tab opens; a test sends through the last one. */
let source = null
class FakeSource {
  constructor() {
    source = this
  }
  close() {}
}
const send = (e) => source.onmessage({ data: JSON.stringify(e) })

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
    version: 11,
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
    expect(wrapper.text()).toContain('Change it under')
    expect(wrapper.html()).toContain('to="/system/general"')
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
    seq: 1,
    logged: '2026-09-20T14:02:11Z',
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

async function events(list, draft = config(), memTotal = 8_000_000_000) {
  // The router searches and narrows the way the page's values say: the Go
  // side is tested against the same values.
  api.proxy.events.mockImplementation(async (params) => ({
    entries: list.filter(
      (e) =>
        (!params.verdict || e.verdict === params.verdict) &&
        matches(params.q ?? '', eventValues(e)),
    ),
    held: list.length,
  }))
  api.systemStats.mockResolvedValue({ memTotal })
  api.logFiles.mockResolvedValue({ enabled: false, logs: [] })
  api.logLimits.mockResolvedValue({ memTotal: 0, ceilings: {} })
  const store = useConfigStore()
  store.draft = draft
  store.saved = structuredClone(draft)
  store.loaded = true
  const wrapper = mount(EventsTab, { global: { stubs } })
  await flushPromises()
  return { wrapper, store }
}

/** The client column of each row, top to bottom. */
const clients = (w) => w.findAll('tbody tr').map((r) => r.findAll('td')[2]?.text())

describe('EventsTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('says so when nothing has matched', async () => {
    const { wrapper } = await events([])
    expect(wrapper.text()).toContain('No events.')
    expect(api.proxy.events).toHaveBeenCalledWith({ limit: 200 }, expect.any(AbortSignal))
  })

  // Only memory holds the events while the router does not write the logs
  // to files, so a restart empties them.
  it('says a restart empties the events unless they go to files', async () => {
    let { wrapper } = await events([event()])
    expect(wrapper.text()).toContain('Kept in memory, so a restart empties it.')
    const draft = config()
    draft.system = { logging: { files: { enabled: true } } }
    ;({ wrapper } = await events([event()], draft))
    expect(wrapper.text()).not.toContain('a restart empties it')
    expect(wrapper.text()).toContain('Older ones are read from the files.')
  })

  // Live is on from the start. Off holds what arrives until it is on again,
  // and an event the first read already holds does not land twice.
  it('lands new events on top while Live is on, and holds them while it is off', async () => {
    const { wrapper } = await events([event({ seq: 7, client: '10.0.0.7' })])
    send(event({ seq: 7, client: '10.0.0.7' }))
    send(event({ seq: 8, client: '10.0.0.8' }))
    await flushPromises()
    expect(clients(wrapper)).toEqual(['10.0.0.8', '10.0.0.7'])

    const live = wrapper.findAll('button').find((b) => b.text() === 'Live')
    expect(live.attributes('aria-pressed')).toBe('true')
    await live.trigger('click')
    send(event({ seq: 9, client: '10.0.0.9' }))
    await flushPromises()
    expect(clients(wrapper)).toEqual(['10.0.0.8', '10.0.0.7'])

    await live.trigger('click')
    expect(clients(wrapper)).toEqual(['10.0.0.9', '10.0.0.8', '10.0.0.7'])
  })

  // The router searches once typing rests, and narrows to a verdict.
  it('asks the router to search and to narrow', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const { wrapper } = await events([
        event({ seq: 2, client: '10.0.0.2', verdict: 'blocked' }),
        event({
          seq: 1,
          client: '10.0.0.1',
          rules: [{ id: 942100, message: 'SQL Injection Attack Detected via libinjection' }],
        }),
      ])
      const box = wrapper.find('input[type=search]')
      const search = async (q) => {
        await box.setValue(q)
        vi.advanceTimersByTime(250)
        await flushPromises()
      }
      await search('942100')
      expect(api.proxy.events).toHaveBeenLastCalledWith(
        { limit: 200, q: '942100' },
        expect.any(AbortSignal),
      )
      expect(clients(wrapper)).toEqual(['10.0.0.1'])
      await search('would block')
      expect(clients(wrapper)).toEqual(['10.0.0.1'])
      await search('nothing-like-this')
      expect(wrapper.text()).toContain('Nothing matches "nothing-like-this".')
      await search('')
      await wrapper.get('select[aria-label="Verdict"]').setValue('blocked')
      await flushPromises()
      expect(clients(wrapper)).toEqual(['10.0.0.2'])
    } finally {
      vi.useRealTimers()
    }
  })

  // The page's strip owns the status read, so it is mounted beside the tab.
  it('says no new events arrive while the proxy is off', async () => {
    const off = config({ services: { proxy: proxy({ enabled: false }) } })
    const { wrapper } = await events([event()], off)
    await strip(status({ running: false }), { draft: off, saved: off })
    expect(wrapper.text()).toContain('The proxy is off, so no new events arrive.')
    expect(clients(wrapper)).toEqual(['192.168.1.55'])
  })

  it('keeps how many events in the draft, and what that costs', async () => {
    const { wrapper, store } = await events([])
    expect(wrapper.text()).toContain('10,000 is the default. In memory, about 15.4 MB when full.')
    await wrapper.find('#events-entries').setValue('100000')
    expect(store.draft.services.proxy.events).toEqual({ entries: 100000 })
    expect(wrapper.text()).toContain('In memory, about 154 MB when full.')
    // Emptied, the block leaves the draft rather than lingering empty.
    await wrapper.find('#events-entries').setValue('')
    expect(store.draft.services.proxy.events).toBeUndefined()
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

  // Vue drops the space between the badge's element and the status's. An
  // event from before the proxy wrote statuses has none.
  it('shows the status beside the verdict', async () => {
    const { wrapper } = await events([
      event({ seq: 2, uri: '/login', verdict: 'blocked', status: 403 }),
      event({ seq: 1, uri: '/login', status: undefined }),
    ])
    const [blocked, older] = wrapper.findAll('tbody tr').map((tr) => tr.findAll('td')[4])
    expect(blocked.text()).toMatch(/^blocked +· 403$/)
    expect(older.text()).toBe('would block')
    expect(wrapper.find('summary').text()).toBe('GET /login')
  })

  // The proxy logs a request when it ends: a row says when that was, and
  // how long a request such as a WebSocket was open before it.
  it('shows when an event was logged, and how long its request was open', async () => {
    const { wrapper } = await events([
      event({ seq: 2, time: '2026-09-20T13:14:00Z' }),
      event({ seq: 1, time: '2026-09-20T14:02:10Z' }),
    ])
    const [socket, request] = wrapper.findAll('tbody tr').map((tr) => tr.findAll('td')[0])
    const logged = new Date('2026-09-20T14:02:11Z').toLocaleString()
    expect(socket.text()).toContain(logged)
    const open = socket.get('div')
    expect(open.text()).toBe('open 48m')
    expect(open.attributes('title')).toBe(
      `Opened ${new Date('2026-09-20T13:14:00Z').toLocaleString()}`,
    )
    expect(request.text()).toBe(logged)
  })

  it('shows a rule that matched twice as one line with a count', async () => {
    const { wrapper } = await events([
      event({
        rules: [
          { id: 942100, message: 'SQL Injection Attack Detected via libinjection' },
          { id: 942100 },
        ],
      }),
    ])
    const chips = wrapper.findAll('.badge.font-mono')
    expect(chips).toHaveLength(1)
    expect(chips[0].text()).toBe('942100')
    expect(wrapper.text()).toContain('×2')
  })

  // The proxy keeps a hundred matches of an event and counts the rest.
  it('says how many matches were not kept', async () => {
    const { wrapper } = await events([event({ moreMatches: 1901 })])
    expect(wrapper.text()).toContain('1,901 more matches not kept.')
    const quiet = await events([event()])
    expect(quiet.wrapper.text()).not.toContain('not kept')
  })

  // An IPv6 client wraps between its halves and nowhere else.
  it('keeps each half of an IPv6 client whole', async () => {
    const { wrapper } = await events([
      event({ seq: 2, client: '2001:db8:4f2a:1c00:9a3e:bd12:77c4:e09f' }),
      event({ seq: 1, client: '192.168.1.55' }),
    ])
    const [v6, v4] = wrapper.findAll('tbody tr').map((tr) => tr.findAll('td')[2])
    expect(v6.findAll('span').map((s) => s.text())).toEqual([
      '2001:db8:4f2a:1c00:',
      '9a3e:bd12:77c4:e09f',
    ])
    expect(v6.findAll('wbr')).toHaveLength(1)
    expect(v6.text()).toBe('2001:db8:4f2a:1c00:9a3e:bd12:77c4:e09f')
    expect(v4.findAll('span').map((s) => s.text())).toEqual(['192.168.1.55'])
  })

  it('offers no exclusion when the site has no profile', async () => {
    const { wrapper } = await events([event()])
    expect(wrapper.text()).toContain('The site has no WAF profile.')
    expect(wrapper.text()).not.toContain('Exclude on this path')
  })

  it('adds a rule to the draft, and opens the dialog to add it on a path', async () => {
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
    const links = wrapper.findAll('button.link-action')
    await links[0].trigger('click')
    expect(store.proxy.wafProfiles[0].exclusions).toEqual([
      { rule: '941100', description: 'XSS Attack Detected' },
    ])
    // The path can be cut down first, so it waits for the dialog.
    await links[1].trigger('click')
    expect(store.proxy.wafProfiles[0].exclusions).toHaveLength(1)
    expect(wrapper.findComponent(ExclusionDialog).props()).toMatchObject({
      open: true,
      profile: 'strict',
      index: -1,
      exclusion: { rule: '941100', path: '/', description: 'XSS Attack Detected' },
    })
  })

  // Clear empties the router's events, files included, and reads them again.
  it('clears the router’s events', async () => {
    const held = [event()]
    const { wrapper } = await events(held)
    api.proxy.clearEvents.mockImplementation(async () => held.splice(0))
    const clear = wrapper.findComponent(ClearLogButton)
    expect(clear.props()).toMatchObject({ name: 'WAF events', noun: 'event', journal: true })
    clear.vm.$emit('confirm')
    await flushPromises()
    expect(api.proxy.clearEvents).toHaveBeenCalled()
    expect(wrapper.text()).toContain('No events.')
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
      global: { stubs: { AppDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
    })
    await flushPromises()
    await wrapper.find('form').trigger('submit')
    expect(store.proxy.wafProfiles[0].exclusions).toEqual([
      { rule: '941100', description: 'XSS Attack Detected' },
    ])
  })

  // An unset threshold used to show 0, when the WAF runs at the default.
  it('shows an unset number as its default and saves only what was set', async () => {
    const profile = { id: 'watch', inboundThreshold: 10 }
    const store = useConfigStore()
    store.draft = config({ services: { proxy: proxy({ wafProfiles: [profile] }) } })
    store.loaded = true
    const wrapper = mount(ProfileDialog, {
      props: { open: true, profile },
      global: { stubs: { AppDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
    })
    await flushPromises()
    expect(wrapper.get('#waf-inbound').element.value).toBe('10')
    for (const [id, fallback] of [
      ['#waf-outbound', '4'],
      ['#waf-body', '12'],
    ]) {
      expect(wrapper.get(id).element.value).toBe('')
      expect(wrapper.get(id).attributes('placeholder')).toBe(fallback)
    }
    await wrapper.find('form').trigger('submit')
    expect(store.proxy.wafProfiles[0]).toEqual({ id: 'watch', inboundThreshold: 10 })
  })

  // A body past the limit is refused unless the profile lets it through,
  // which only the switch says.
  it('passes larger bodies only when switched to', async () => {
    const profile = { id: 'vault', bodyLimitMB: 64 }
    const store = useConfigStore()
    store.draft = config({ services: { proxy: proxy({ wafProfiles: [profile] }) } })
    store.loaded = true
    const wrapper = mount(ProfileDialog, {
      props: { open: true, profile },
      global: { stubs: { AppDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
    })
    await flushPromises()
    expect(wrapper.text()).toContain('A larger one is refused, an upload too.')
    const pass = wrapper
      .findAll('input[type="checkbox"]')
      .find((i) => i.element.parentElement.textContent.includes('Let larger bodies through'))
    expect(pass.element.checked).toBe(false)
    await pass.setValue(true)
    expect(wrapper.text()).toContain('Past it, a body goes through with the rest unread.')
    await wrapper.find('form').trigger('submit')
    expect(store.proxy.wafProfiles[0]).toEqual({
      id: 'vault',
      bodyLimitMB: 64,
      passLargeBodies: true,
    })
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

/** A line of the access list. */
function line(over = {}) {
  return {
    id: 'wan',
    enabled: true,
    zone: 'wan',
    action: 'accept',
    ports: ['https'],
    source: {},
    ...over,
  }
}

/** Mounts a component with the draft of our choosing, dialogs inline. */
function withDraft(component, draft, props = {}) {
  const store = useConfigStore()
  store.draft = draft
  store.saved = structuredClone(draft)
  store.loaded = true
  const wrapper = mount(component, {
    props,
    global: {
      stubs: {
        ...stubs,
        AppDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        'transition-group': false,
      },
    },
  })
  return { wrapper, store }
}

describe('ServiceTab access list', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('says nothing reaches the proxy until a rule accepts it', () => {
    const { wrapper } = withDraft(ServiceTab, config())
    expect(wrapper.text()).toContain('Nothing reaches the proxy until a rule accepts it.')
  })

  // Rules are read within their zone, so the table groups them by zone and
  // Move steps past the other zones' rules.
  it('lists the rules by zone and moves one among its zone', async () => {
    const draft = config({
      services: {
        proxy: proxy({
          access: [
            line({ id: 'a', description: 'first' }),
            line({ id: 'l', zone: 'lan', description: 'inside' }),
            line({
              id: 'b',
              description: 'second',
              action: 'drop',
              source: { alias: 'us', notAddresses: true },
            }),
          ],
        }),
      },
    })
    const { wrapper, store } = withDraft(ServiceTab, draft)
    const rows = () => wrapper.findAll('tbody tr').map((r) => r.findAll('td')[5].text())
    expect(rows()).toEqual(['first', 'second', 'inside'])
    expect(wrapper.findAll('tbody tr')[1].text()).toContain('not @us')
    await wrapper.get('button[aria-label="Move second up"]').trigger('click')
    expect(store.draft.services.proxy.access.map((a) => a.id)).toEqual(['b', 'l', 'a'])
    expect(rows()).toEqual(['second', 'first', 'inside'])
    expect(wrapper.get('button[aria-label="Move inside up"]').attributes('disabled')).toBeDefined()
  })

  it('switches a rule off from its row', async () => {
    const draft = config({
      services: { proxy: proxy({ access: [line({ description: 'sites' })] }) },
    })
    const { wrapper, store } = withDraft(ServiceTab, draft)
    await wrapper.get('input[aria-label="Enable sites"]').setValue(false)
    expect(store.draft.services.proxy.access[0].enabled).toBe(false)
  })
})

describe('AccessDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('writes a rule: its zone, its ports by name, its source and its verdict', async () => {
    const draft = config({
      aliases: [{ name: 'us', type: 'geoip', entries: ['us'] }],
      services: {
        proxy: proxy({
          routes: [{ id: 'imap', enabled: true, protocol: 'tcp', port: 993, upstreams: [] }],
        }),
      },
    })
    const { wrapper, store } = withDraft(AccessDialog, draft, { open: true, line: null })
    await flushPromises()
    // A new rule starts on the external zone, HTTPS from anywhere.
    expect(wrapper.get('#access-zone').element.value).toBe('wan')
    await wrapper.get('input[value="http"]').setValue(true)
    await wrapper.get('input[value="imap"]').setValue(true)
    await wrapper.get('#source-mode').setValue('alias')
    await wrapper.get('#source-alias').setValue('us')
    await wrapper.get('#access-desc').setValue('Sites from the US')
    await wrapper.get('form').trigger('submit')
    const [saved] = store.draft.services.proxy.access
    expect(saved).toMatchObject({
      enabled: true,
      zone: 'wan',
      ports: ['http', 'https'],
      routes: ['imap'],
      source: { alias: 'us' },
      action: 'accept',
      description: 'Sites from the US',
    })
    expect(saved.id).toMatch(/^access-/)
  })

  it('says HTTP alone reaches a site as a redirect', async () => {
    const { wrapper } = withDraft(AccessDialog, config(), {
      open: true,
      line: line({ ports: ['http'] }),
    })
    await flushPromises()
    expect(wrapper.text()).toContain('Sites send HTTP to HTTPS unless they serve plain HTTP.')
    await wrapper.get('input[value="https"]').setValue(true)
    expect(wrapper.text()).not.toContain('Sites send HTTP to HTTPS')
  })
})

describe('RoutesTab', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('says where each route is reachable', () => {
    const draft = config({
      services: {
        proxy: proxy({
          routes: [
            { id: 'imap', enabled: true, protocol: 'tcp', port: 993, upstreams: [] },
            { id: 'dns', enabled: true, protocol: 'udp', port: 53, upstreams: [] },
          ],
          access: [line({ routes: ['imap'] }), line({ id: 'd', routes: ['dns'], action: 'drop' })],
        }),
      },
    })
    const { wrapper } = withDraft(RoutesTab, draft)
    const cells = wrapper.findAll('td[data-label="Access"]').map((c) => c.text())
    expect(cells).toEqual(['wan', 'No rule opens it'])
  })

  // A route renamed or deleted takes its name out of the access list, and
  // a rule left naming nothing goes with it.
  it('keeps the access list in step with the routes', () => {
    const draft = config({
      services: {
        proxy: proxy({
          routes: [{ id: 'imap', enabled: true, protocol: 'tcp', port: 993, upstreams: [] }],
          access: [line({ routes: ['imap'] }), line({ id: 'only', ports: [], routes: ['imap'] })],
        }),
      },
    })
    const { store } = withDraft(RoutesTab, draft)
    store.upsertProxyRoute({ ...draft.services.proxy.routes[0], id: 'mail' }, 'imap')
    expect(store.draft.services.proxy.access.map((a) => a.routes)).toEqual([['mail'], ['mail']])
    expect(store.routeDependents('mail')).toEqual(['wan', 'only'])
    store.removeProxyRoute('mail')
    expect(store.draft.services.proxy.access).toEqual([line()])
  })
})

describe('Allow from', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // A hosts alias with its entries written in it may stand beside the
  // prefixes; a fetched one stays in the text, where the check refuses it.
  it('offers the small aliases and keeps the rest as typed', async () => {
    const route = {
      id: 'imap',
      enabled: true,
      protocol: 'tcp',
      port: 993,
      upstreams: [{ address: '10.0.0.2:993' }],
      allowFrom: ['office', '192.168.0.0/16', 'us'],
    }
    const draft = config({
      aliases: [
        { name: 'office', type: 'hosts', entries: ['198.51.100.0/24'] },
        { name: 'feed', type: 'hosts', entries: ['https://example.com/list'] },
        { name: 'us', type: 'geoip', entries: ['us'] },
      ],
      services: { proxy: proxy({ routes: [route] }) },
    })
    const { wrapper, store } = withDraft(RouteDialog, draft, { open: true, route })
    await flushPromises()
    expect(wrapper.get('#route-allow').element.value).toBe('192.168.0.0/16\nus')
    const offered = wrapper.findAll('label').filter((l) => ['office', 'feed'].includes(l.text()))
    expect(offered.map((l) => l.text())).toEqual(['office'])
    expect(offered[0].get('input').element.checked).toBe(true)
    await wrapper.get('#route-allow').setValue('192.168.0.0/16')
    await wrapper.get('form').trigger('submit')
    expect(store.draft.services.proxy.routes[0].allowFrom).toEqual(['192.168.0.0/16', 'office'])
  })
})
