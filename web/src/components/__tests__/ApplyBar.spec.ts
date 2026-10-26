import type { VueWrapper } from '@vue/test-utils'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ApplyBar from '@/components/ApplyBar.vue'
import { ApiError, api, type Session, type Status } from '@/lib/api'
import type { ApplyResult, ConfirmReply, Plan } from '@/lib/types.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import { useSystemStore } from '@/stores/system'
import { configOf, fixture, must } from '@/test/fixtures'

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
  ApiError: class ApiError extends Error {
    status: number
    issues: { path: string; message: string }[]
    code: string
    constructor(status: number, message: string, issues = [], code = '') {
      super(message)
      this.status = status
      this.issues = issues
      this.code = code
    }
  },
}))

enableAutoUnmount(afterEach)

const OLD = configOf({ version: 3, system: { hostname: 'old' } })
const NEW = configOf({ version: 3, system: { hostname: 'new' } })
/** What GET /config answers: the saved configuration and its revision. */
const AT_OLD = { config: OLD, revision: 'r1' }
const AT_NEW = { config: NEW, revision: 'r2' }
const STALE =
  "This router's configuration changed since this draft was read. Reload to see what is saved; this draft is dropped."

/** What the status answers, less the fields these tests do not look at. */
type Answer = Omit<Partial<Status>, 'pending'> & { pending?: Partial<Status['pending']> }
const statusIs = (answer: Answer) => vi.mocked(api.status).mockResolvedValue(answer as Status)

let pending: { since: string; deadline: string }

/** A router whose configuration is OLD, with an apply of NEW awaiting confirmation. */
async function router({ applying = true } = {}) {
  pending = {
    since: new Date().toISOString(),
    deadline: new Date(Date.now() + 60_000).toISOString(),
  }
  vi.mocked(api.config.get).mockResolvedValue(AT_OLD)
  statusIs(applying ? { configured: true, pending } : { configured: true })
  await useConfigStore().load()
  await useSystemStore().refresh()
}

/** The draft, which every test here has loaded before it edits or reads it. */
function draft() {
  const d = useConfigStore().draft
  if (!d) throw new Error('no draft')
  return d
}

const button = (w: VueWrapper, name: string) => w.findAll('button').find((b) => b.text() === name)

describe('ApplyBar', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    vi.clearAllMocks()
    vi.mocked(api.config.diff).mockResolvedValue([])
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

    vi.mocked(api.config.confirm).mockResolvedValue(fixture<ConfirmReply>({}))
    statusIs({ configured: true })
    vi.mocked(api.config.get).mockResolvedValue(AT_NEW)
    await button(w, 'Confirm')?.trigger('click')
    await flushPromises()

    expect(w.text()).toContain('Confirmed and saved.')
    const config = useConfigStore()
    expect(draft().system.hostname).toBe('new')
    expect(config.dirty).toBe(false)
  })

  // Another tab confirmed it. The saved configuration changed, which only
  // a confirm does, so this tab must not say it ran out.
  it('says confirmed when another tab confirmed', async () => {
    await router()
    const w = mount(ApplyBar)
    await flushPromises()

    statusIs({ configured: true })
    vi.mocked(api.config.get).mockResolvedValue(AT_NEW)
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(w.text()).toContain('Confirmed and saved.')
    expect(draft().system.hostname).toBe('new')
  })

  it('keeps the draft of an apply that ran out, to try again', async () => {
    await router({ applying: false })
    draft().system.hostname = 'mine'
    const w = mount(ApplyBar)
    await flushPromises()

    vi.mocked(api.config.check).mockResolvedValue(fixture<Plan>({}))
    vi.mocked(api.config.apply).mockResolvedValue(fixture<ApplyResult>(pending))
    statusIs({ configured: true, pending })
    await button(w, 'Apply with 60s confirmation')?.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('awaiting confirmation')

    await vi.advanceTimersByTimeAsync(61_000)
    statusIs({ configured: true })
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(w.text()).toContain('Not confirmed in time.')

    await vi.advanceTimersByTimeAsync(3000)
    expect(w.text()).toContain('Unapplied changes.')
    expect(draft().system.hostname).toBe('mine')
  })

  // A browser drops every request in flight when the computer's addresses
  // change, and an apply can change them. The status says whether the
  // apply this tab sent is the one awaiting confirmation.
  describe('an apply whose reply was lost', () => {
    const lost = new TypeError('Failed to fetch')

    /**
     * A draft of NEW whose apply reaches the router, or not, and never
     * answers. The status fails to answer `silent` times first.
     */
    async function sent({ made = true, silent = 0 } = {}) {
      await router({ applying: false })
      const config = useConfigStore()
      draft().system.hostname = 'new'
      vi.mocked(api.config.check).mockResolvedValue(fixture<Plan>({}))
      vi.mocked(api.config.apply).mockImplementation(async (_cfg, _base, _seconds, tag) => {
        if (made)
          statusIs({
            configured: true,
            pending: { ...pending, tag },
          })
        for (let i = 0; i < silent; i++) vi.mocked(api.status).mockRejectedValueOnce(lost)
        throw lost
      })
      const w = mount(ApplyBar)
      await flushPromises()
      return { config, w }
    }

    it('offers the confirm, which saves the draft', async () => {
      const { config, w } = await sent()
      await button(w, 'Apply with 60s confirmation')?.trigger('click')
      await flushPromises()
      expect(w.text()).toContain('awaiting confirmation')
      expect(w.find('[role="alert"]').exists()).toBe(false)

      vi.mocked(api.config.confirm).mockResolvedValue(fixture<ConfirmReply>({}))
      vi.mocked(api.config.get).mockResolvedValue(AT_NEW)
      statusIs({ configured: true })
      await button(w, 'Confirm')?.trigger('click')
      await flushPromises()
      expect(w.text()).toContain('Confirmed and saved.')
      expect(config.dirty).toBe(false)
    })

    it('asks again until the router answers', async () => {
      const { w } = await sent({ silent: 2 })
      await button(w, 'Apply with 60s confirmation')?.trigger('click')
      await flushPromises()
      expect(w.text()).not.toContain('awaiting confirmation')
      await vi.advanceTimersByTimeAsync(2000)
      await flushPromises()
      expect(w.text()).toContain('awaiting confirmation')
    })

    it('says nothing was applied when the router has no such apply', async () => {
      const { w } = await sent({ made: false })
      await button(w, 'Apply with 60s confirmation')?.trigger('click')
      await flushPromises()
      expect(w.get('[role="alert"]').text()).toBe('Nothing was applied: Failed to fetch.')
      expect(button(w, 'Apply with 60s confirmation')).toBeDefined()
      expect(draft().system.hostname).toBe('new')
    })

    it('says so when the router stays silent', async () => {
      const { w } = await sent({ made: false })
      vi.mocked(api.status).mockRejectedValue(lost)
      await button(w, 'Apply with 60s confirmation')?.trigger('click')
      await vi.advanceTimersByTimeAsync(21_000)
      await flushPromises()
      expect(w.get('[role="alert"]').text()).toBe(
        'The router did not answer: Failed to fetch. If it took the apply, the previous configuration comes back within 60s.',
      )
    })

    // Another tab's apply was waiting, so this one was refused, and the
    // refusal was lost. When that apply is confirmed, this draft has not
    // been saved by it.
    it("does not take another tab's apply for its own", async () => {
      const { w } = await sent({ made: false })
      draft().system.hostname = 'mine'
      vi.mocked(api.config.apply).mockImplementation(async () => {
        statusIs({
          configured: true,
          pending: { ...pending, tag: 'apply-other' },
        })
        throw lost
      })
      await button(w, 'Apply with 60s confirmation')?.trigger('click')
      await flushPromises()
      expect(w.text()).toContain('awaiting confirmation')

      statusIs({ configured: true })
      vi.mocked(api.config.get).mockResolvedValue(AT_NEW)
      await vi.advanceTimersByTimeAsync(2000)
      await flushPromises()
      expect(w.text()).toContain('Confirmed and saved.')
      await vi.advanceTimersByTimeAsync(3000)
      expect(w.get('[role="alert"]').text()).toBe(STALE)
      expect(draft().system.hostname).toBe('mine')
    })
  })

  describe('a confirm whose reply was lost', () => {
    const lost = new TypeError('Failed to fetch')

    it('is confirmed when the router says so', async () => {
      await router()
      const w = mount(ApplyBar)
      await flushPromises()
      vi.mocked(api.config.confirm).mockRejectedValue(lost)
      statusIs({ configured: true })
      vi.mocked(api.config.get).mockResolvedValue(AT_NEW)
      await button(w, 'Confirm')?.trigger('click')
      await flushPromises()
      expect(w.text()).toContain('Confirmed and saved.')
      expect(w.text()).not.toContain('Failed to fetch')
    })

    it('can be sent again when it never arrived', async () => {
      await router()
      const w = mount(ApplyBar)
      await flushPromises()
      vi.mocked(api.config.confirm).mockRejectedValue(lost)
      await button(w, 'Confirm')?.trigger('click')
      await flushPromises()
      expect(w.text()).toContain('Failed to fetch')
      expect(must(button(w, 'Confirm')).attributes('disabled')).toBeUndefined()
    })
  })

  describe('a draft read before another save', () => {
    /** A draft of OLD at r1 with an edit of its own. */
    async function edited() {
      await router({ applying: false })
      const config = useConfigStore()
      draft().system.hostname = 'mine'
      return config
    }

    it('says so as soon as the saved configuration moves, and reloads behind a confirm', async () => {
      const config = await edited()
      const w = mount(ApplyBar)
      await flushPromises()
      expect(w.text()).toContain('Unapplied changes.')

      vi.mocked(api.config.get).mockResolvedValue(AT_NEW)
      await config.resync()
      await flushPromises()
      expect(w.get('[role="alert"]').text()).toBe(STALE)
      expect(button(w, 'Apply with 60s confirmation')).toBeUndefined()
      expect(button(w, 'Discard')).toBeUndefined()

      const confirm = useConfirmStore()
      await button(w, 'Reload')?.trigger('click')
      expect(confirm.request).toMatchObject({
        question: 'Reload the saved configuration?',
        description: 'This draft is dropped.',
        confirmLabel: 'Reload',
      })
      confirm.settle(false)
      await flushPromises()
      expect(draft().system.hostname).toBe('mine')
      expect(w.get('[role="alert"]').text()).toBe(STALE)

      await button(w, 'Reload')?.trigger('click')
      confirm.settle(true)
      await flushPromises()
      expect(config.draft).toEqual(NEW)
      expect(config.base).toBe('r2')
      expect(w.text()).toBe('')
    })

    it('says so when the server refuses the apply as stale', async () => {
      await edited()
      const w = mount(ApplyBar)
      await flushPromises()

      vi.mocked(api.config.check).mockResolvedValue(fixture<Plan>({}))
      vi.mocked(api.config.apply).mockRejectedValue(
        new ApiError(409, 'the configuration changed since this draft was read', [], 'stale'),
      )
      vi.mocked(api.config.get).mockResolvedValue(AT_NEW)
      await button(w, 'Apply with 60s confirmation')?.trigger('click')
      await flushPromises()

      expect(api.config.apply).toHaveBeenCalledWith(
        { ...OLD, system: { hostname: 'mine' } },
        'r1',
        60,
        expect.any(String),
      )
      expect(w.findAll('[role="alert"]').map((a) => a.text())).toEqual([STALE])
      expect(button(w, 'Reload')).toBeDefined()
      expect(draft().system.hostname).toBe('mine')
    })

    it('keeps the line when the refusal comes before the save can be read', async () => {
      await edited()
      const w = mount(ApplyBar)
      await flushPromises()

      vi.mocked(api.config.check).mockResolvedValue(fixture<Plan>({}))
      vi.mocked(api.config.apply).mockRejectedValue(
        new ApiError(409, 'the configuration changed since this draft was read', [], 'stale'),
      )
      await button(w, 'Apply with 60s confirmation')?.trigger('click')
      await flushPromises()
      expect(w.get('[role="alert"]').text()).toBe(STALE)
    })

    it('shows another conflict as the server put it', async () => {
      await edited()
      const w = mount(ApplyBar)
      await flushPromises()

      vi.mocked(api.config.check).mockResolvedValue(fixture<Plan>({}))
      vi.mocked(api.config.apply).mockRejectedValue(
        new ApiError(409, 'another apply is awaiting confirmation'),
      )
      await button(w, 'Apply with 60s confirmation')?.trigger('click')
      await flushPromises()
      expect(w.get('[role="alert"]').text()).toBe('another apply is awaiting confirmation')
      expect(button(w, 'Apply with 60s confirmation')).toBeDefined()
      expect(button(w, 'Reload')).toBeUndefined()
    })
  })

  describe('what this version would apply differently', () => {
    const drift = { parts: ['Firewall', 'Reverse proxy'], changes: 3 }

    /** A router whose saved configuration this version renders differently. */
    async function updated() {
      vi.mocked(api.config.get).mockResolvedValue(AT_OLD)
      statusIs({ configured: true, drift })
      vi.mocked(api.config.drift).mockResolvedValue({
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

      await button(w, 'Show 3 changes')?.trigger('click')
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
      draft().system.hostname = 'mine'
      const w = mount(ApplyBar)
      await flushPromises()
      expect(w.text()).toContain('Unapplied changes.')
      expect(button(w, 'Discard')).toBeDefined()
    })

    it('is not shown to a viewer, who cannot apply', async () => {
      useAuthStore().user = { username: 'look', role: 'viewer' } as Session
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
      vi.mocked(api.config.check).mockResolvedValue(fixture<Plan>({}))
      vi.mocked(api.config.apply).mockResolvedValue(
        fixture<ApplyResult>({ pending: true, deadline: waiting.deadline }),
      )
      statusIs({ configured: true, pending: waiting })
      await button(w, 'Apply with 60s confirmation')?.trigger('click')
      await flushPromises()
      expect(api.config.apply).toHaveBeenCalledWith(OLD, 'r1', 60, expect.any(String))
      expect(w.text()).toContain('awaiting confirmation')

      // Confirmed in another tab: the saved configuration is as it was, and
      // the drift is gone from the status.
      statusIs({ configured: true })
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
      const reads = vi.mocked(api.status).mock.calls.length
      await vi.advanceTimersByTimeAsync(15_000)
      expect(vi.mocked(api.status).mock.calls.length).toBe(reads + 1)
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
    let observed: Element[]
    beforeEach(() => {
      observed = []
      window.ResizeObserver = class implements ResizeObserver {
        fn: ResizeObserverCallback
        constructor(fn: ResizeObserverCallback) {
          this.fn = fn
        }
        observe(el: Element) {
          observed.push(el)
          this.fn([], this)
        }
        unobserve() {}
        disconnect() {}
      }
    })
    afterEach(() => {
      Reflect.deleteProperty(window, 'ResizeObserver')
      Reflect.deleteProperty(window, 'matchMedia')
      document.documentElement.style.removeProperty('--dock')
    })

    async function dirty(narrow: boolean) {
      window.matchMedia = vi.fn(
        () =>
          ({
            matches: narrow,
            addEventListener: vi.fn(),
            removeEventListener: vi.fn(),
          }) as unknown as MediaQueryList,
      )
      await router({ applying: false })
      draft().system.hostname = 'mine'
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
