import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ApplyBar from '@/components/ApplyBar.vue'
import { api } from '@/lib/api'
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

  it('leaves the wizard to show its own apply', async () => {
    route.name = 'wizard'
    await router()
    const w = mount(ApplyBar)
    await flushPromises()
    expect(w.find('[role="status"]').exists()).toBe(false)
  })
})
