import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import LogPage from '@/views/firewall/LogPage.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: { log: { recent: vi.fn() } },
}))

/** The page opens a stream; nothing here tests it, so it is inert. */
class FakeSource {
  close() {}
}

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

  it('opens on the refusals, hiding what was let through', async () => {
    const w = await open(log)
    expect(matched(w)).toEqual(['block-iot', 'no-smb', 'guest default'])
  })

  it('shows what was let through on request, and everything together', async () => {
    const w = await open(log)
    await w.get('select').setValue('allowed')
    expect(matched(w)).toEqual(['allow-web'])
    await w.get('select').setValue('all')
    expect(matched(w)).toHaveLength(4)
  })

  // Green for a rule match would read as "allowed" on a rule that drops,
  // so the colour follows the verdict instead.
  it('colours the row by what happened, not by what matched', async () => {
    const w = await open(log)
    await w.get('select').setValue('all')
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
    await w.get('input').setValue('allow-web')
    expect(rows(w)[0].text()).toContain('Nothing matches "allow-web"')
  })
})
