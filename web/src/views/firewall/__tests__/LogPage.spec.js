import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { fwlogValues } from '@/lib/fwlog'
import { SETTLE_MS } from '@/lib/log'
import { matches } from '@/lib/search'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import LogPage from '@/views/firewall/LogPage.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: {
    log: { entries: vi.fn(), clear: vi.fn() },
    systemStats: vi.fn(() => Promise.resolve({ memTotal: 0 })),
    logFiles: vi.fn(() => Promise.resolve({ enabled: false, logs: [] })),
  },
}))

/** The stream the page opens; a test sends through the last one. */
let source = null
class FakeSource {
  constructor() {
    source = this
  }
  close() {}
}
const send = (e) => source.onmessage({ data: JSON.stringify(e) })

let seq = 0
function entry(over = {}) {
  return {
    seq: ++seq,
    time: '2026-09-19T12:00:00Z',
    kind: 'rule',
    proto: 'tcp',
    src: '10.0.0.5',
    dst: '10.0.0.1',
    length: 60,
    ...over,
  }
}

/** Body rows; the test renderer stubs the transition group that wraps them. */
const rows = (w) => w.findAll('tr').filter((r) => r.find('td').exists())
const matched = (w) => rows(w).map((r) => r.findAll('td')[2].text())

/**
 * A router holding these packets, newest first: it narrows and searches
 * the way the Go side does, which internal/fwlog tests against the same
 * values.
 */
function serve(held) {
  api.log.entries.mockImplementation(async (params) => {
    const shown = held.filter((e) => {
      if (params.show && e.action) {
        if (params.show === 'blocked' ? e.action === 'accept' : e.action !== 'accept') return false
      }
      return matches(params.q ?? '', fwlogValues(e))
    })
    return { entries: shown, more: false, held: held.length, oldest: held.at(-1)?.time }
  })
}

async function open(held) {
  serve(held)
  const w = mount(LogPage)
  await flushPromises()
  return w
}

describe('LogPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
    const store = useConfigStore()
    const draft = { system: { management: { webPort: 9443, sshPort: 22 } }, services: {} }
    store.draft = draft
    store.saved = structuredClone(draft)
  })

  const log = [
    entry({ ruleId: 'allow-web', action: 'accept', src: '1' }),
    entry({ ruleId: 'block-iot', action: 'drop', src: '2' }),
    entry({ ruleId: 'no-smb', action: 'reject', src: '3' }),
    entry({ kind: 'zone-drop', zone: 'guest', action: 'drop', src: '4' }),
  ]

  it('opens on everything, whatever happened to it', async () => {
    const w = await open(log)
    expect(matched(w)).toEqual(['allow-web', 'block-iot', 'no-smb', 'guest default'])
  })

  it('narrows to the refusals, or to what was let through', async () => {
    const w = await open(log)
    await w.get('select').setValue('blocked')
    await flushPromises()
    expect(api.log.entries).toHaveBeenLastCalledWith(
      { show: 'blocked', limit: 200 },
      expect.any(AbortSignal),
    )
    expect(matched(w)).toEqual(['block-iot', 'no-smb', 'guest default'])
    await w.get('select').setValue('allowed')
    await flushPromises()
    expect(matched(w)).toEqual(['allow-web'])
  })

  // The firewall refuses encrypted DNS and holds a scanner in rules of its
  // own, which the zone's log-drops setting now reaches.
  it('names the drops the firewall makes on its own account', async () => {
    const w = await open([
      entry({ kind: 'block-doh', action: 'drop', src: '1' }),
      entry({ kind: 'protect-scanner', action: 'drop', src: '2' }),
      entry({ kind: 'protect-synflood', action: 'drop', src: '3' }),
    ])
    expect(matched(w)).toEqual(['DNS over HTTPS', 'port scan', 'connection flood'])
  })

  // Green for a rule match would read as "allowed" on a rule that drops,
  // so the colour follows the verdict instead.
  it('colours the row by what happened, not by what matched', async () => {
    const w = await open(log)
    const badge = (i) => rows(w)[i].findAll('td')[1].get('span')
    expect(badge(0).text()).toBe('accept')
    expect(badge(0).classes()).toContain('badge-ok')
    expect(badge(1).classes()).toContain('badge-bad')
    expect(badge(2).classes()).toContain('badge-warn')
  })

  // A line of the proxy's access list is named as its page names it.
  it('names a proxy access rule by its description', async () => {
    const store = useConfigStore()
    store.draft.services = {
      proxy: { access: [{ id: 'access-x1', description: 'Sites from home' }] },
    }
    const w = await open([
      entry({ kind: 'proxy', ruleId: 'access-x1', action: 'accept', src: '1' }),
      entry({ kind: 'proxy', ruleId: 'gone', action: 'drop', src: '2' }),
    ])
    expect(matched(w)).toEqual(['proxy: Sites from home', 'proxy: gone'])
  })

  it('names the source guards', async () => {
    const w = await open([entry({ kind: 'block-private', action: 'drop' })])
    expect(matched(w)).toEqual(['private source'])
  })

  // A ruleset written before the verdict went into the prefix says nothing
  // about it. Hiding those rows would empty the page on a router that has
  // not applied since the upgrade.
  it('keeps a packet whose verdict was never recorded, whichever view is on', async () => {
    const w = await open([entry({ ruleId: 'old-rule' })])
    expect(matched(w)).toEqual(['old-rule'])
    expect(rows(w)[0].findAll('td')[1].text()).toBe('unknown')
    await w.get('select').setValue('allowed')
    expect(matched(w)).toEqual(['old-rule'])
  })

  // The router searches, once typing rests, within the chosen view: a
  // text match outside it still does not drag it in.
  it('narrows the chosen view further with the search', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const w = await open(log)
      await w.get('input[type=search]').setValue('no-smb')
      vi.advanceTimersByTime(SETTLE_MS)
      await flushPromises()
      expect(api.log.entries).toHaveBeenLastCalledWith(
        { limit: 200, q: 'no-smb' },
        expect.any(AbortSignal),
      )
      expect(matched(w)).toEqual(['no-smb'])
      await w.get('select').setValue('blocked')
      await w.get('input[type=search]').setValue('allow-web')
      vi.advanceTimersByTime(SETTLE_MS)
      await flushPromises()
      expect(rows(w)[0].text()).toContain('Nothing matches "allow-web"')
    } finally {
      vi.useRealTimers()
    }
  })

  it('says what the log holds', async () => {
    const w = await open(log)
    expect(w.text()).toMatch(/4 entries back to .+\. Kept in memory/)
  })

  // Older packets are read from the table's foot, and reading them is
  // reading history: Live stops.
  it('reads older packets from its foot', async () => {
    const page = Array.from({ length: 200 }, (_, i) => entry({ ruleId: `r${i}`, action: 'accept' }))
    api.log.entries.mockResolvedValueOnce({ entries: page, more: true, next: 3, held: 204 })
    const w = mount(LogPage)
    await flushPromises()
    serve(log)
    await w
      .findAll('button')
      .find((b) => b.text() === 'Load more')
      .trigger('click')
    await flushPromises()
    expect(api.log.entries).toHaveBeenLastCalledWith(
      { limit: 200, before: 3 },
      expect.any(AbortSignal),
    )
    expect(matched(w).slice(200)).toEqual(['allow-web', 'block-iot', 'no-smb', 'guest default'])
    expect(w.text()).toContain('Start of the log.')
    const live = w.findAll('button').find((b) => b.text() === 'Live')
    expect(live.attributes('aria-pressed')).toBe('false')
  })

  // Clear empties the router's log, its files included, not only the
  // page, and asks first. Only an admin clears.
  it('clears the router’s log on Clear, once asked', async () => {
    useAuthStore().user = { username: 'root', role: 'admin' }
    const held = [...log]
    const w = await open(held)
    api.log.clear.mockImplementation(async () => held.splice(0))
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    await w
      .findAll('button')
      .find((b) => b.text() === 'Clear')
      .trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({ question: 'Clear the firewall log?' }),
    )
    expect(api.log.clear).toHaveBeenCalled()
    expect(rows(w)[0].text()).toContain('No packets logged.')
  })

  // A restart empties it unless the logs go to files as well.
  it('says a restart empties it only while there are no files', async () => {
    let w = await open(log)
    expect(w.text()).toContain('Kept in memory, so a restart empties it.')
    useConfigStore().saved.system.logging = { files: { enabled: true } }
    w = await open(log)
    expect(w.text()).not.toContain('a restart empties it')
    expect(w.text()).toContain('Older ones are read from the files.')
  })

  // Off holds what arrives, so nothing is missed; on shows it.
  it('holds new packets while Live is off', async () => {
    const w = await open(log)
    send(entry({ ruleId: 'first', action: 'accept' }))
    await flushPromises()
    expect(matched(w)[0]).toBe('first')

    const live = w.findAll('button').find((b) => b.text() === 'Live')
    expect(live.attributes('aria-pressed')).toBe('true')
    await live.trigger('click')
    send(entry({ ruleId: 'held', action: 'accept' }))
    await flushPromises()
    expect(matched(w)).not.toContain('held')

    await live.trigger('click')
    expect(matched(w)[0]).toBe('held')
  })
})

describe('LogPage settings', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
  })

  // The log's own settings sit on its page, and leave the draft as saved
  // when put back.
  it('keeps the entries, the days and dropped packets in the draft', async () => {
    const store = useConfigStore()
    const draft = { system: { management: { webPort: 9443, sshPort: 22 } }, services: {} }
    store.draft = draft
    store.saved = structuredClone(draft)
    const w = await open([])
    expect(w.text()).toContain('50,000 is the default. About 17.5 MB of memory when full.')
    await w.get('#firewall-entries').setValue('200000')
    await w.get('#firewall-days').setValue('3')
    const drops = w
      .findAll('input[type=checkbox]')
      .find((i) => i.element.closest('label, div')?.textContent.includes('Log dropped packets'))
    await drops.setValue(true)
    expect(store.draft.system.management).toMatchObject({
      firewallLog: { entries: 200000, days: 3 },
      logDefaultDrops: true,
    })
    await w.get('#firewall-entries').setValue('')
    await w.get('#firewall-days').setValue('')
    await drops.setValue(false)
    expect(store.dirty).toBe(false)
  })
})
