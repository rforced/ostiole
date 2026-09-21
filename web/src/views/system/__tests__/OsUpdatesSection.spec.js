import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfirmStore } from '@/stores/confirm'
import OsUpdatesSection from '@/views/system/OsUpdatesSection.vue'

vi.mock('@/lib/api', () => ({
  api: {
    systemUpdates: {
      status: vi.fn(),
      check: vi.fn(),
      apply: vi.fn(),
      reboot: vi.fn(),
    },
  },
  errorMessage: (e) => String(e),
}))

/** A Fedora router with two updates waiting, one of them a security fix. */
const waiting = {
  manager: 'dnf',
  distro: 'Fedora Linux 44',
  available: true,
  securityCapable: true,
  excludeSupported: true,
  mode: 'manual',
  running: false,
  lastCheck: '2026-09-21T12:00:00Z',
  pending: {
    security: 1,
    packages: [
      { name: 'openssl', from: '3.2.1', to: '3.2.2', repo: 'updates', security: true },
      { name: 'vim', from: '9.1.1', to: '9.1.2', repo: 'updates' },
    ],
  },
}

const installButton = (w) => w.findAll('button').find((b) => b.text().startsWith('Install'))

async function mountWith(status) {
  api.systemUpdates.status.mockResolvedValue(status)
  const w = mount(OsUpdatesSection)
  await flushPromises()
  return w
}

describe('OsUpdatesSection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    // Only the poll's interval: flushPromises needs a real setTimeout.
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    api.systemUpdates.status.mockReset()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('offers what is waiting when nothing is running', async () => {
    const w = await mountWith(waiting)
    const button = installButton(w)
    // Manual mode installs everything, because pressing the button is
    // asking for it.
    expect(button.text()).toBe('Install all updates')
    expect(button.attributes('disabled')).toBeUndefined()
    expect(button.attributes('aria-busy')).toBe('false')
  })

  it('follows the mode when it is security only', async () => {
    const w = await mountWith({ ...waiting, mode: 'security' })
    expect(installButton(w).text()).toBe('Install security updates')
  })

  it('says so on the button while the transaction runs', async () => {
    const w = await mountWith({ ...waiting, running: true })
    const button = installButton(w)
    // A package manager reports no progress, so the button is the whole
    // account of it.
    expect(button.text()).toBe('Installing…')
    expect(button.attributes('disabled')).toBeDefined()
    expect(button.attributes('aria-busy')).toBe('true')
    expect(button.find('.animate-spin').exists()).toBe(true)
  })

  it('says so on the button before the first poll has seen it running', async () => {
    vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const w = await mountWith(waiting)
    // apply returns as soon as the router is claimed, and the status it
    // answers with is the first thing that says so. Without install.busy
    // covering the request itself the button would sit there looking dead.
    let claimed
    api.systemUpdates.apply.mockReturnValue(
      new Promise((resolve) => {
        claimed = resolve
      }),
    )

    await installButton(w).trigger('click')
    await flushPromises()
    expect(installButton(w).text()).toBe('Installing…')

    claimed({ ...waiting, running: true })
    await flushPromises()
    // And keeps saying it once running has taken over from install.busy.
    expect(installButton(w).text()).toBe('Installing…')
  })

  it('does nothing when the confirmation is refused', async () => {
    vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(false)
    const w = await mountWith(waiting)
    await installButton(w).trigger('click')
    await flushPromises()
    expect(api.systemUpdates.apply).not.toHaveBeenCalled()
    expect(installButton(w).text()).toBe('Install all updates')
  })
})
