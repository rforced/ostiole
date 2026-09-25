import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import LogPage from '@/views/firewall/LogPage.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: { log: { recent: vi.fn() } },
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

function entry(over = {}) {
  return {
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

async function open(recent) {
  api.log.recent.mockResolvedValue(recent)
  const w = mount(LogPage)
  await flushPromises()
  return w
}

describe('LogPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', FakeSource)
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
    expect(matched(w)).toEqual(['block-iot', 'no-smb', 'guest default'])
    await w.get('select').setValue('allowed')
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

  it('narrows the chosen view further with the text filter', async () => {
    const w = await open(log)
    await w.get('input').setValue('no-smb')
    expect(matched(w)).toEqual(['no-smb'])
    // The filter searches the whole entry, so a text match outside the
    // current view still does not drag it in.
    await w.get('select').setValue('blocked')
    await w.get('input').setValue('allow-web')
    expect(rows(w)[0].text()).toContain('Nothing matches "allow-web"')
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
