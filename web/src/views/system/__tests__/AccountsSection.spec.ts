import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import type { Role } from '@/lib/types.gen'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'
import AccountsSection from '@/views/system/AccountsSection.vue'
import { sessionOf, must } from '@/test/fixtures'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: {
    users: {
      list: vi.fn(),
      create: vi.fn(),
      remove: vi.fn(),
      setRole: vi.fn(),
      setPassword: vi.fn(),
      rename: vi.fn(),
      clearMFA: vi.fn(),
    },
    auth: { me: vi.fn() },
  },
}))

// The dialogs live in a portal, which mount() cannot reach; open them in
// place so a test can type into them.
const AppDialogStub = {
  props: ['open', 'title', 'description'],
  template: '<div v-if="open"><slot /><slot name="footer" /></div>',
}

const OFF = { totp: false, passkeys: 0 }
const APP = { totp: true, passkeys: 0 }

const account = (username: string, role: Role, mfa = OFF) => ({
  username,
  role,
  mfa,
  createdAt: '2026-09-01T00:00:00Z',
  updatedAt: '2026-09-01T00:00:00Z',
})

async function open(accounts = [account('admin', 'admin'), account('watcher', 'viewer')]) {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  vi.mocked(api.users.list).mockResolvedValue(accounts)
  const auth = useAuthStore()
  auth.user = sessionOf({ username: 'admin', expires: '2099-01-01T00:00:00Z' })
  const wrapper = mount(AccountsSection, {
    global: { stubs: { AppDialog: AppDialogStub, MfaCard: true } },
  })
  await flushPromises()
  return wrapper
}

const row = (wrapper: VueWrapper, username: string) =>
  must(wrapper.findAll('tbody tr').find((tr) => tr.get('td').text().startsWith(username)))

const findButton = (scope: VueWrapper | DOMWrapper<Element>, text: string) =>
  scope.findAll('button').find((b) => b.text() === text)
const button = (scope: VueWrapper | DOMWrapper<Element>, text: string) =>
  must(findButton(scope, text))

describe('AccountsSection', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('lists every account with its role', async () => {
    const wrapper = await open()
    expect(row(wrapper, 'admin').get<HTMLSelectElement>('select').element.value).toBe('admin')
    expect(row(wrapper, 'watcher').get<HTMLSelectElement>('select').element.value).toBe('viewer')
  })

  // The whole point of the section: you cannot take your own admin away,
  // so the controls that would are not there to click.
  it('will not let you change or delete your own account', async () => {
    const mine = row(await open(), 'admin')
    expect(mine.get('select').attributes('disabled')).toBeDefined()
    expect(mine.findAll('button').map((b) => b.text())).toEqual(['Rename'])
  })

  it('offers the full set of actions on somebody else', async () => {
    const theirs = row(await open(), 'watcher')
    expect(theirs.get('select').attributes('disabled')).toBeUndefined()
    expect(theirs.findAll('button').map((b) => b.text())).toEqual([
      'Rename',
      'Set password',
      'Delete',
    ])
  })

  it('creates an account and redraws from what the server returned', async () => {
    const wrapper = await open([account('admin', 'admin')])
    await button(wrapper, 'Add account').trigger('click')
    await wrapper.get('#user-name').setValue('operator')
    await wrapper.get('#user-password').setValue('correct horse battery')
    await wrapper.get('#user-role').setValue('operator')
    vi.mocked(api.users.create).mockResolvedValue([
      account('admin', 'admin'),
      account('operator', 'operator'),
    ])

    await must(wrapper.get<HTMLInputElement>('#user-name').element.form).dispatchEvent(
      new Event('submit'),
    )
    await flushPromises()

    expect(api.users.create).toHaveBeenCalledWith({
      username: 'operator',
      password: 'correct horse battery',
      role: 'operator',
    })
    expect(row(wrapper, 'operator').get<HTMLSelectElement>('select').element.value).toBe('operator')
  })

  it('refuses a password the server would reject anyway', async () => {
    const wrapper = await open([account('admin', 'admin')])
    await button(wrapper, 'Add account').trigger('click')
    await wrapper.get('#user-name').setValue('operator')
    await wrapper.get('#user-password').setValue('short')
    expect(button(wrapper, 'Create account').attributes('disabled')).toBeDefined()
  })

  // A rename keeps the session, so the name the rest of the UI shows has to
  // follow it rather than wait for the next sign-in.
  it('follows its own rename', async () => {
    const wrapper = await open()
    const auth = useAuthStore()
    vi.mocked(api.users.rename).mockResolvedValue([
      account('alice', 'admin'),
      account('watcher', 'viewer'),
    ])
    vi.mocked(api.auth.me).mockResolvedValue(
      sessionOf({ username: 'alice', expires: '2099-01-01T00:00:00Z' }),
    )

    await button(row(wrapper, 'admin'), 'Rename').trigger('click')
    await wrapper.get('#rename-to').setValue('alice')
    await must(wrapper.get<HTMLInputElement>('#rename-to').element.form).dispatchEvent(
      new Event('submit'),
    )
    await flushPromises()

    expect(api.users.rename).toHaveBeenCalledWith('admin', 'alice')
    expect(must(auth.user).username).toBe('alice')
  })

  it('shows what the server said when a change is refused', async () => {
    const wrapper = await open()
    vi.mocked(api.users.rename).mockRejectedValue(
      new Error('an account with that name already exists'),
    )

    await button(row(wrapper, 'watcher'), 'Rename').trigger('click')
    await wrapper.get('#rename-to').setValue('admin')
    await must(wrapper.get<HTMLInputElement>('#rename-to').element.form).dispatchEvent(
      new Event('submit'),
    )
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('already exists')
  })

  const mfaCell = (wrapper: VueWrapper, username: string) =>
    row(wrapper, username).get('td[data-label="MFA"]')

  it('says what each account signs in with', async () => {
    const wrapper = await open([
      account('admin', 'admin'),
      account('watcher', 'viewer', APP),
      account('lone', 'viewer', { totp: true, passkeys: 1 }),
      account('keys', 'operator', { totp: true, passkeys: 3 }),
    ])
    expect(mfaCell(wrapper, 'admin').text()).toBe('Off')
    expect(mfaCell(wrapper, 'admin').get('span').classes()).toContain('text-ink-muted')
    expect(mfaCell(wrapper, 'watcher').text()).toBe('App')
    expect(mfaCell(wrapper, 'lone').text()).toBe('App, 1 passkey')
    expect(mfaCell(wrapper, 'keys').text()).toBe('App, 3 passkeys')
  })

  it("clears another account's MFA once the confirm says yes", async () => {
    const wrapper = await open([account('admin', 'admin'), account('watcher', 'viewer', APP)])
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    vi.mocked(api.users.clearMFA).mockResolvedValue([])
    vi.mocked(api.users.list).mockResolvedValue([
      account('admin', 'admin'),
      account('watcher', 'viewer'),
    ])

    await button(row(wrapper, 'watcher'), 'Clear MFA').trigger('click')
    await flushPromises()

    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({ question: 'Clear MFA for watcher?' }),
    )
    expect(api.users.clearMFA).toHaveBeenCalledWith('watcher')
    expect(mfaCell(wrapper, 'watcher').text()).toBe('Off')
    expect(findButton(row(wrapper, 'watcher'), 'Clear MFA')).toBeUndefined()
  })

  it('leaves MFA when the confirm is declined', async () => {
    const wrapper = await open([account('admin', 'admin'), account('watcher', 'viewer', APP)])
    vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(false)
    await button(row(wrapper, 'watcher'), 'Clear MFA').trigger('click')
    await flushPromises()
    expect(api.users.clearMFA).not.toHaveBeenCalled()
  })

  // Your own is cleared from the Multi-factor authentication card, on your password.
  it('offers no clear on your own account, nor on one without MFA', async () => {
    const wrapper = await open([account('admin', 'admin', APP), account('watcher', 'viewer')])
    expect(findButton(row(wrapper, 'admin'), 'Clear MFA')).toBeUndefined()
    expect(findButton(row(wrapper, 'watcher'), 'Clear MFA')).toBeUndefined()
  })
})
