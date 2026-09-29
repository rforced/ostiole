import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
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
const checkButton = (w) => w.findAll('button').find((b) => b.text().startsWith('Check now'))
const waitingCard = (w) => w.findAll('section').find((s) => s.text().startsWith('Waiting'))
const waitingNames = (w) =>
  waitingCard(w)
    .findAll('tbody span.font-mono')
    .map((n) => n.text())

/** A draft with the given update settings, as a signed-in page has one. */
function draftWith(mode, exclude) {
  useConfigStore().replaceDraft({
    version: 11,
    updates: { system: { mode, exclude } },
    zones: [],
    interfaces: [],
    rules: [],
  })
}

async function mountWith(status, role = 'admin') {
  useAuthStore().user = { username: role, role }
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

  // Installing is an admin's; an operator can still look.
  it('keeps the install from an operator', async () => {
    const w = await mountWith(waiting, 'operator')
    expect(installButton(w).attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('Only an admin can install updates')
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

  // Arch has no security channel, so an unset mode means manual there,
  // and the choice shown is the one the router runs.
  it('shows the mode an unset configuration means on this router', async () => {
    useConfigStore().replaceDraft({
      version: 11,
      updates: {},
      zones: [],
      interfaces: [],
      rules: [],
    })
    const arch = { ...waiting, manager: 'pacman', securityCapable: false, defaultMode: 'manual' }
    const w = await mountWith(arch)
    expect(w.find('#os-upd-mode-manual').element.checked).toBe(true)
    expect(w.find('#os-upd-mode-security').element.checked).toBe(false)
  })

  // The Waiting card is what the install button installs.
  it('lists everything on manual', async () => {
    const w = await mountWith(waiting)
    expect(waitingNames(w)).toEqual(['openssl', 'vim'])
    expect(waitingCard(w).text()).toContain('1 of them security.')
  })

  it('lists only the security fixes on security', async () => {
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(false)
    const w = await mountWith({ ...waiting, mode: 'security' })
    expect(waitingNames(w)).toEqual(['openssl'])
    expect(waitingCard(w).text()).not.toContain('of them security')
    await installButton(w).trigger('click')
    expect(ask).toHaveBeenCalledWith(expect.objectContaining({ question: 'Install 1 update?' }))
  })

  it('says so when security has nothing to install', async () => {
    const vim = waiting.pending.packages[1]
    const w = await mountWith({
      ...waiting,
      mode: 'security',
      pending: { security: 0, packages: [vim] },
    })
    expect(waitingCard(w)).toBeUndefined()
    expect(w.text()).toContain('No security updates are waiting.')
    expect(installButton(w).attributes('disabled')).toBeDefined()
  })

  // Nothing refreshes what the last check and install found on disabled,
  // so none of it shows and nothing can be checked or installed.
  it('shows only when it last checked on disabled', async () => {
    const w = await mountWith({
      ...waiting,
      mode: 'disabled',
      rebootRequired: true,
      rebootReason: 'kernel 6.12.1 is installed but 6.12.0 is running',
      lastRun: '2026-09-20T04:30:00Z',
      lastError: 'dnf upgrade failed',
      checkError: 'no route to the mirror',
    })
    expect(w.text()).toContain('Last checked')
    expect(waitingCard(w)).toBeUndefined()
    expect(checkButton(w)).toBeUndefined()
    expect(installButton(w)).toBeUndefined()
    for (const gone of ['wants a reboot', 'Last run', 'dnf upgrade failed', 'last check failed']) {
      expect(w.text()).not.toContain(gone)
    }
  })

  // The page follows the mode as it is chosen, before it is applied.
  it('follows the mode in the draft', async () => {
    draftWith('manual')
    const w = await mountWith(waiting)
    expect(waitingNames(w)).toEqual(['openssl', 'vim'])

    await w.get('#os-upd-mode-security').setValue(true)
    expect(waitingNames(w)).toEqual(['openssl'])
    expect(installButton(w).text()).toBe('Install security updates')

    await w.get('#os-upd-mode-disabled').setValue(true)
    expect(waitingCard(w)).toBeUndefined()
    expect(checkButton(w)).toBeUndefined()
    expect(w.text()).toContain('Nothing is checked or installed, on a schedule or by hand.')

    await w.get('#os-upd-mode-all').setValue(true)
    expect(waitingNames(w)).toEqual(['openssl', 'vim'])
    expect(checkButton(w).exists()).toBe(true)
  })

  // Only a check that answered can say nothing is waiting.
  it('calls nothing up to date before a check has answered', async () => {
    const empty = { security: 0, packages: null }
    let w = await mountWith({ ...waiting, lastCheck: undefined, pending: empty })
    expect(w.text()).not.toContain('up to date')
    w = await mountWith({ ...waiting, checkError: 'no route to the mirror', pending: empty })
    expect(w.text()).not.toContain('up to date')
    w = await mountWith({ ...waiting, pending: empty })
    expect(w.text()).toContain('Everything is up to date.')
  })

  // A package on Never upgrade stays listed, and Install leaves it alone.
  it('marks what is never upgraded and installs the rest', async () => {
    draftWith('manual', ['vim*'])
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    api.systemUpdates.apply.mockResolvedValue(waiting)
    const w = await mountWith(waiting)
    const badges = waitingCard(w)
      .findAll('tbody tr')
      .map((r) => r.findAll('.badge').map((b) => b.text()))
    expect(badges).toEqual([['security'], ['never upgrade']])

    await installButton(w).trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledWith(expect.objectContaining({ question: 'Install 1 update?' }))
    // As the page shows it, whether or not it is applied yet.
    expect(api.systemUpdates.apply).toHaveBeenLastCalledWith(false, ['vim*'])

    useConfigStore().setUpdates('system', { exclude: ['openssl', 'vim'] })
    await flushPromises()
    expect(installButton(w).attributes('disabled')).toBeDefined()
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
