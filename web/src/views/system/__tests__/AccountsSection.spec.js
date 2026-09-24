import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import AccountsSection from '@/views/system/AccountsSection.vue'

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
    },
    auth: { me: vi.fn() },
  },
}))

// The dialogs live in a portal, which mount() cannot reach; open them in
// place so a test can type into them.
const AppDialogStub = {
  props: ['open', 'title', 'description'],
  template: '<div v-if="open"><slot /></div>',
}

const account = (username, role) => ({
  username,
  role,
  createdAt: '2026-09-01T00:00:00Z',
  updatedAt: '2026-09-01T00:00:00Z',
})

async function open(accounts = [account('admin', 'admin'), account('watcher', 'viewer')]) {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  api.users.list.mockResolvedValue(accounts)
  const auth = useAuthStore()
  auth.user = { username: 'admin', expires: '2099-01-01T00:00:00Z' }
  const wrapper = mount(AccountsSection, { global: { stubs: { AppDialog: AppDialogStub } } })
  await flushPromises()
  return wrapper
}

const row = (wrapper, username) =>
  wrapper.findAll('tbody tr').find((tr) => tr.get('td').text().startsWith(username))

const button = (scope, text) => scope.findAll('button').find((b) => b.text() === text)

describe('AccountsSection', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('lists every account with its role', async () => {
    const wrapper = await open()
    expect(row(wrapper, 'admin').get('select').element.value).toBe('admin')
    expect(row(wrapper, 'watcher').get('select').element.value).toBe('viewer')
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
    api.users.create.mockResolvedValue([account('admin', 'admin'), account('operator', 'operator')])

    await wrapper.get('#user-name').element.form.dispatchEvent(new Event('submit'))
    await flushPromises()

    expect(api.users.create).toHaveBeenCalledWith({
      username: 'operator',
      password: 'correct horse battery',
      role: 'operator',
    })
    expect(row(wrapper, 'operator').get('select').element.value).toBe('operator')
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
    api.users.rename.mockResolvedValue([account('josh', 'admin'), account('watcher', 'viewer')])
    api.auth.me.mockResolvedValue({ username: 'josh', expires: '2099-01-01T00:00:00Z' })

    await button(row(wrapper, 'admin'), 'Rename').trigger('click')
    await wrapper.get('#rename-to').setValue('josh')
    await wrapper.get('#rename-to').element.form.dispatchEvent(new Event('submit'))
    await flushPromises()

    expect(api.users.rename).toHaveBeenCalledWith('admin', 'josh')
    expect(auth.user.username).toBe('josh')
  })

  it('shows what the server said when a change is refused', async () => {
    const wrapper = await open()
    api.users.rename.mockRejectedValue(new Error('an account with that name already exists'))

    await button(row(wrapper, 'watcher'), 'Rename').trigger('click')
    await wrapper.get('#rename-to').setValue('admin')
    await wrapper.get('#rename-to').element.form.dispatchEvent(new Event('submit'))
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('already exists')
  })
})
