import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ApplyBar from '@/components/ApplyBar.vue'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useSystemStore } from '@/stores/system'

const route = vi.hoisted(() => ({ name: 'rules' }))
vi.mock('vue-router', () => ({ useRoute: () => route }))
vi.mock('@/lib/api', () => ({
  api: {
    status: vi.fn(),
    config: {
      get: vi.fn(),
      check: vi.fn(),
      apply: vi.fn(),
      confirm: vi.fn(),
      revert: vi.fn(),
      diff: vi.fn(),
      drift: vi.fn(),
    },
  },
  ApiError: class ApiError extends Error {},
}))

const OLD = { version: 3, system: { hostname: 'old' } }
const NEW = { version: 3, system: { hostname: 'new' } }

let pending

/** A router whose configuration is OLD, with an apply of NEW awaiting confirmation. */
async function router({ applying = true } = {}) {
  pending = {
    since: new Date().toISOString(),
    deadline: new Date(Date.now() + 60_000).toISOString(),
  }
  api.config.get.mockResolvedValue(OLD)
  api.status.mockResolvedValue(applying ? { configured: true, pending } : { configured: true })
  await useConfigStore().load()
  await useSystemStore().refresh()
}

const button = (w, name) => w.findAll('button').find((b) => b.text() === name)

describe('ApplyBar', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    vi.clearAllMocks()
    api.config.diff.mockResolvedValue([])
    route.name = 'rules'
  })
  afterEach(() => vi.useRealTimers())

  it('shows an apply made before a reload, on any page', async () => {
    await router()
    const w = mount(ApplyBar)
    await flushPromises()
    expect(w.findAll('[role="status"]')).toHaveLength(1)
    expect(w.text()).toContain('awaiting confirmation')
  })

  it('brings the draft up to date when that apply is confirmed here', async () => {
    await router()
    const w = mount(ApplyBar)
    await flushPromises()

    api.config.confirm.mockResolvedValue({})
    api.status.mockResolvedValue({ configured: true })
    api.config.get.mockResolvedValue(NEW)
    await button(w, 'Confirm').trigger('click')
    await flushPromises()

    expect(w.text()).toContain('Confirmed and saved.')
    const config = useConfigStore()
    expect(config.draft.system.hostname).toBe('new')
    expect(config.dirty).toBe(false)
  })

  // Another tab confirmed it. The saved configuration changed, which only
  // a confirm does, so this tab must not say it ran out.
  it('says confirmed when another tab confirmed', async () => {
    await router()
    const w = mount(ApplyBar)
    await flushPromises()

    api.status.mockResolvedValue({ configured: true })
    api.config.get.mockResolvedValue(NEW)
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(w.text()).toContain('Confirmed and saved.')
    expect(useConfigStore().draft.system.hostname).toBe('new')
  })

  it('keeps the draft of an apply that ran out, to try again', async () => {
    await router({ applying: false })
    const config = useConfigStore()
    config.draft.system.hostname = 'mine'
    const w = mount(ApplyBar)
    await flushPromises()

    api.config.check.mockResolvedValue({})
    api.config.apply.mockResolvedValue(pending)
    api.status.mockResolvedValue({ configured: true, pending })
    await button(w, 'Apply with 60s confirmation').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('awaiting confirmation')

    await vi.advanceTimersByTimeAsync(61_000)
    api.status.mockResolvedValue({ configured: true })
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(w.text()).toContain('Not confirmed in time.')

    await vi.advanceTimersByTimeAsync(3000)
    expect(w.text()).toContain('Unapplied changes.')
    expect(config.draft.system.hostname).toBe('mine')
  })

  describe('what this version would apply differently', () => {
    const drift = { parts: ['Firewall', 'Reverse proxy'], changes: 3 }

    /** A router whose saved configuration this version renders differently. */
    async function updated() {
      api.config.get.mockResolvedValue(OLD)
      api.status.mockResolvedValue({ configured: true, drift })
      api.config.drift.mockResolvedValue({
        parts: drift.parts,
        changes: [
          { path: 'firewall', kind: 'removed', before: '\tan old rule' },
          { path: 'proxy/caddy.json', kind: 'added', after: '  "site": "two"' },
        ],
        more: 1,
      })
      await useConfigStore().load()
      await useSystemStore().refresh()
    }

    it('offers it, with the lines it is made of', async () => {
      await updated()
      const w = mount(ApplyBar)
      await flushPromises()
      expect(w.text()).toContain('Not applied with this version: Firewall, Reverse proxy.')
      expect(button(w, 'Discard')).toBeUndefined()
      expect(api.config.drift).not.toHaveBeenCalled()

      await button(w, 'Show 3 changes').trigger('click')
      await flushPromises()
      expect(api.config.drift).toHaveBeenCalledOnce()
      const lines = w.findAll('li').map((li) => li.text())
      // The mark is a cell of its own beside the line.
      expect(lines).toEqual([
        '−firewall: an old rule',
        '+proxy/caddy.json: "site": "two"',
        'and 1 more',
      ])
    })

    it('leaves it to a draft, whose apply brings it in too', async () => {
      await updated()
      useConfigStore().draft.system.hostname = 'mine'
      const w = mount(ApplyBar)
      await flushPromises()
      expect(w.text()).toContain('Unapplied changes.')
      expect(button(w, 'Discard')).toBeDefined()
    })

    it('is not shown to a viewer, who cannot apply', async () => {
      useAuthStore().user = { username: 'look', role: 'viewer' }
      await updated()
      const w = mount(ApplyBar)
      await flushPromises()
      expect(w.text()).toBe('')
    })

    it('applies the saved configuration, and its confirm clears it', async () => {
      await updated()
      const w = mount(ApplyBar)
      await flushPromises()

      const waiting = {
        since: new Date().toISOString(),
        deadline: new Date(Date.now() + 60_000).toISOString(),
      }
      api.config.check.mockResolvedValue({})
      api.config.apply.mockResolvedValue({ pending: true, deadline: waiting.deadline })
      api.status.mockResolvedValue({ configured: true, pending: waiting })
      await button(w, 'Apply with 60s confirmation').trigger('click')
      await flushPromises()
      expect(api.config.apply).toHaveBeenCalledWith(OLD, 60)
      expect(w.text()).toContain('awaiting confirmation')

      // Confirmed in another tab: the saved configuration is as it was, and
      // the drift is gone from the status.
      api.status.mockResolvedValue({ configured: true })
      await vi.advanceTimersByTimeAsync(2000)
      await flushPromises()
      expect(w.text()).toContain('Confirmed and saved.')
      await vi.advanceTimersByTimeAsync(3000)
      expect(w.text()).toBe('')
    })

    it('reads the status again while shown', async () => {
      await updated()
      mount(ApplyBar)
      await flushPromises()
      const reads = api.status.mock.calls.length
      await vi.advanceTimersByTimeAsync(15_000)
      expect(api.status.mock.calls.length).toBe(reads + 1)
    })
  })

  it('leaves the wizard to show its own apply', async () => {
    route.name = 'wizard'
    await router()
    const w = mount(ApplyBar)
    await flushPromises()
    expect(w.find('[role="status"]').exists()).toBe(false)
  })

  // Docked over the bottom of a narrow screen, the bar says how much of the
  // page it covers. Above lg it sits in the flow and measures nothing.
  describe('docked', () => {
    let observed
    beforeEach(() => {
      observed = []
      window.ResizeObserver = class {
        constructor(fn) {
          this.fn = fn
        }
        observe(el) {
          observed.push(el)
          this.fn()
        }
        disconnect() {}
      }
    })
    afterEach(() => {
      delete window.ResizeObserver
      delete window.matchMedia
      document.documentElement.style.removeProperty('--dock')
    })

    async function dirty(narrow) {
      window.matchMedia = vi.fn(() => ({
        matches: narrow,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }))
      await router({ applying: false })
      useConfigStore().draft.system.hostname = 'mine'
      const w = mount(ApplyBar)
      await flushPromises()
      return w
    }

    it('says how much of a narrow screen it covers', async () => {
      const w = await dirty(true)
      expect(observed).toHaveLength(1)
      expect(document.documentElement.style.getPropertyValue('--dock')).toMatch(/px$/)
      w.unmount()
      expect(document.documentElement.style.getPropertyValue('--dock')).toBe('')
    })

    it('measures nothing on a wide one', async () => {
      await dirty(false)
      expect(observed).toHaveLength(0)
      expect(document.documentElement.style.getPropertyValue('--dock')).toBe('')
    })
  })
})
