import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import UpdatesSection from '@/views/system/UpdatesSection.vue'

vi.mock('@/lib/api', () => ({
  api: { health: vi.fn(), update: { check: vi.fn(), status: vi.fn(), apply: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

/** What the last check left behind: 0.9.2 is waiting. */
const waiting = {
  lastCheck: '2026-09-19T12:00:00Z',
  channel: 'stable',
  current: 'v0.9.1',
  latest: 'v0.9.2',
  available: true,
  release: {
    tag: 'v0.9.2',
    publishedAt: '2026-09-19T12:00:00Z',
    url: 'https://x.invalid',
    notes: '',
  },
}

const installButton = (w) => w.findAll('button').find((b) => b.text().startsWith('Install'))

/** Mounts the section on a router running 0.9.1 with 0.9.2 going in. */
async function mountInstalling() {
  api.health.mockResolvedValue({ version: 'v0.9.1' })
  api.update.status.mockResolvedValue({
    status: { state: 'installing', version: 'v0.9.2' },
    check: waiting,
  })
  const w = mount(UpdatesSection)
  await flushPromises()
  return w
}

/**
 * The daemon comes back as another version between two polls, so the page
 * never sees the restarting state and the process that answers next has
 * never heard of the update.
 */
async function restartsInto(version) {
  api.health.mockResolvedValue({ version })
  api.update.status.mockResolvedValue({ status: { state: 'idle' }, check: waiting })
  await vi.advanceTimersByTimeAsync(1500)
  await flushPromises()
}

describe('UpdatesSection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    // Only the poll's interval: flushPromises needs a real setTimeout.
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    api.health.mockReset()
    api.update.status.mockReset()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('reads a version it did not start with as the update landing', async () => {
    const w = await mountInstalling()
    expect(w.text()).toContain('installing')
    await restartsInto('v0.9.2')
    expect(w.text()).toContain('Updated to')
    expect(w.text()).toContain('v0.9.2')
    expect(w.text()).toContain('Reload the page')
    // And stops saying it is installing what it has installed.
    expect(w.text()).not.toContain('installing')
  })

  it('stops offering the release it is now running', async () => {
    const w = await mountInstalling()
    expect(installButton(w)).toBeDefined()
    await restartsInto('v0.9.2')
    expect(installButton(w)).toBeUndefined()
    // Nor does it call a page a version behind up to date.
    expect(w.text()).not.toContain('Up to date')
  })

  it('stops polling once the daemon has moved on', async () => {
    await mountInstalling()
    await restartsInto('v0.9.2')
    const calls = api.update.status.mock.calls.length
    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()
    expect(api.update.status.mock.calls.length).toBe(calls)
  })

  it('offers the waiting release while the version is the one it loaded', async () => {
    api.health.mockResolvedValue({ version: 'v0.9.1' })
    api.update.status.mockResolvedValue({ status: { state: 'idle' }, check: waiting })
    const w = mount(UpdatesSection)
    await flushPromises()
    expect(installButton(w)?.text()).toBe('Install v0.9.2')
  })
})
