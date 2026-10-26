import { type DOMWrapper, type VueWrapper, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'

import { ApiError, type Mfa, type Passkey, api } from '@/lib/api'
import * as webauthn from '@/lib/webauthn'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'
import MfaCard from '@/views/account/MfaCard.vue'
import { must, sessionOf } from '@/test/fixtures'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {
    status: number
    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  },
  api: {
    auth: {
      mfa: vi.fn(),
      removePasskey: vi.fn(),
      renamePasskey: vi.fn(),
      mfaClear: vi.fn(),
      me: vi.fn(),
      totpStart: vi.fn(),
    },
  },
}))
vi.mock('@/lib/webauthn', () => ({ available: vi.fn() }))

// The dialogs live in a portal, which mount() cannot reach; open them in place.
const AppDialogStub = {
  props: ['open', 'title', 'description'],
  template:
    '<div v-if="open" role="dialog"><h2>{{ title }}</h2><p>{{ description }}</p><slot /><slot name="footer" /></div>',
}

const APP = { enabledAt: '2026-09-20T10:00:00Z', lastUsed: '2026-10-09T10:00:00Z' }

const passkey = (id: string, name: string): Passkey => ({
  id,
  name,
  rpId: 'edge.example',
  createdAt: '2026-10-01T10:00:00Z',
  lastUsed: '2026-10-05T10:00:00Z',
})

const mfa = (over: Partial<Mfa> = {}): Mfa => ({
  totp: APP,
  passkeys: [],
  rpId: 'edge.example',
  ...over,
})

async function open(state: Mfa, role = 'viewer') {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  vi.mocked(webauthn.available).mockReturnValue(true)
  vi.mocked(api.auth.mfa).mockResolvedValue(state)
  vi.mocked(api.auth.me).mockResolvedValue(sessionOf({ username: 'sam', role }))
  useAuthStore().user = sessionOf({ username: 'sam', role })
  const wrapper = mount(MfaCard, { global: { stubs: { AppDialog: AppDialogStub } } })
  await flushPromises()
  return wrapper
}

type Scope = Pick<DOMWrapper<Element>, 'findAll'>

const findButton = (scope: Scope, text: string) =>
  scope.findAll('button').find((b) => b.text() === text)
const button = (scope: Scope, text: string) => {
  const found = findButton(scope, text)
  if (!found) throw new Error(`no button ${text}`)
  return found
}
const disabled = (b: DOMWrapper<Element>) => b.attributes('disabled') !== undefined
const group = (wrapper: VueWrapper, name: string) => {
  const found = wrapper.findAll('fieldset').find((f) => f.get('legend').text() === name)
  if (!found) throw new Error(`no group ${name}`)
  return found
}
const badge = (wrapper: VueWrapper) => wrapper.get('.badge').text()
const intro = (wrapper: VueWrapper) => wrapper.get('h2 + p').text()
const dialog = (wrapper: VueWrapper) => wrapper.find('[role="dialog"]')

describe('MfaCard', () => {
  it('starts with the authenticator app, and says why a passkey has to wait', async () => {
    const wrapper = await open(mfa({ totp: null }))
    expect(badge(wrapper)).toBe('off')
    expect(intro(wrapper)).toBe('Sign-in asks for your password alone.')
    const app = group(wrapper, 'Authenticator app')
    expect(app.text()).toContain('Not set up.')
    expect(disabled(button(app, 'Set up authenticator app'))).toBe(false)
    expect(findButton(app, 'Remove')).toBeUndefined()
    const keys = group(wrapper, 'Passkeys')
    expect(disabled(button(keys, 'Add passkey'))).toBe(true)
    expect(keys.text()).toContain('Set up an authenticator app first.')
    expect(keys.text()).toContain('No passkeys.')
  })

  it('shows when the app was set up and last used, with Replace and Remove', async () => {
    const wrapper = await open(mfa())
    expect(badge(wrapper)).toBe('on')
    expect(intro(wrapper)).toBe(
      'Sign-in asks for a code from your authenticator app after your password.',
    )
    const app = group(wrapper, 'Authenticator app')
    expect(app.text()).toContain('Set up')
    expect(app.text()).toContain('Last used')
    expect(app.text()).not.toContain('never')
    expect(button(app, 'Replace')).toBeDefined()
    expect(button(app, 'Remove')).toBeDefined()
  })

  it('says never for an app not used yet', async () => {
    const wrapper = await open(mfa({ totp: { enabledAt: APP.enabledAt } }))
    expect(group(wrapper, 'Authenticator app').text()).toContain('never')
  })

  it('replaces the app through the setup dialog', async () => {
    const wrapper = await open(mfa())
    await button(group(wrapper, 'Authenticator app'), 'Replace').trigger('click')
    await flushPromises()
    expect(dialog(wrapper).get('h2').text()).toBe('Replace authenticator app')
    expect(api.auth.totpStart).toHaveBeenCalledOnce()
  })

  it('will not make a passkey for an address that is not a name', async () => {
    const wrapper = await open(mfa({ rpId: '' }))
    const keys = group(wrapper, 'Passkeys')
    expect(disabled(button(keys, 'Add passkey'))).toBe(true)
    expect(keys.text()).toContain('Open the UI by its host name to add a passkey.')
  })

  it('will not make a passkey where the browser cannot', async () => {
    setActivePinia(createPinia())
    vi.mocked(webauthn.available).mockReturnValue(false)
    vi.mocked(api.auth.mfa).mockResolvedValue(mfa())
    const wrapper = mount(MfaCard, { global: { stubs: { AppDialog: AppDialogStub } } })
    await flushPromises()
    expect(group(wrapper, 'Passkeys').text()).toContain(
      'This browser cannot make passkeys on this page.',
    )
  })

  it('adds a passkey once the app is set up and the page has a name', async () => {
    const wrapper = await open(mfa())
    const keys = group(wrapper, 'Passkeys')
    expect(disabled(button(keys, 'Add passkey'))).toBe(false)
    await button(keys, 'Add passkey').trigger('click')
    expect(wrapper.find('#passkey-name').exists()).toBe(true)
    expect(wrapper.find('#passkey-password').exists()).toBe(true)
  })

  it('lists passkeys with the host name they were made for', async () => {
    const wrapper = await open(mfa({ passkeys: [passkey('a1', 'Laptop')] }))
    expect(intro(wrapper)).toBe(
      'Sign-in takes a passkey on its own, or your password and then a code from your authenticator app or a passkey.',
    )
    expect(wrapper.find('input[type="checkbox"]').exists()).toBe(false)
    const cells = wrapper.get('tbody tr').findAll('td')
    expect(must(cells[0]).text()).toBe('Laptop')
    expect(must(cells[1]).text()).toBe('edge.example')
    expect(wrapper.findAll('thead th').map((th) => th.text())).toEqual([
      'Passkey',
      'Host name',
      'Added',
      'Last used',
      '',
    ])
  })

  it('renames a passkey and shows the new name', async () => {
    const wrapper = await open(mfa({ passkeys: [passkey('a1', 'Laptop')] }))
    await button(wrapper.get('tbody tr'), 'Rename').trigger('click')
    expect(wrapper.get<HTMLInputElement>('#passkey-rename-name').element.value).toBe('Laptop')
    await wrapper.get<HTMLInputElement>('#passkey-rename-name').setValue(' Desk ')
    vi.mocked(api.auth.renamePasskey).mockResolvedValue(passkey('a1', 'Desk'))
    vi.mocked(api.auth.mfa).mockResolvedValue(mfa({ passkeys: [passkey('a1', 'Desk')] }))

    await wrapper.get('#passkey-rename-form').trigger('submit')
    await flushPromises()

    expect(api.auth.renamePasskey).toHaveBeenCalledWith('a1', 'Desk')
    expect(wrapper.get('tbody tr td').text()).toBe('Desk')
    expect(api.auth.me).toHaveBeenCalled()
  })

  it('removes a passkey once the confirm says yes, and lists what is left', async () => {
    const wrapper = await open(mfa({ passkeys: [passkey('a1', 'Laptop'), passkey('b2', 'Phone')] }))
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    vi.mocked(api.auth.removePasskey).mockResolvedValue(undefined)
    vi.mocked(api.auth.mfa).mockResolvedValue(mfa({ passkeys: [passkey('b2', 'Phone')] }))
    const rows = () => wrapper.findAll('tbody tr')
    expect(rows().map((r) => r.get('td').text())).toEqual(['Laptop', 'Phone'])

    await button(must(rows()[0]), 'Remove').trigger('click')
    await flushPromises()

    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({ question: 'Remove the passkey Laptop?' }),
    )
    expect(api.auth.removePasskey).toHaveBeenCalledWith('a1')
    expect(rows().map((r) => r.get('td').text())).toEqual(['Phone'])
  })

  it('keeps the passkey when the confirm is declined', async () => {
    const wrapper = await open(mfa({ passkeys: [passkey('a1', 'Laptop')] }))
    vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(false)
    await button(wrapper.get('tbody tr'), 'Remove').trigger('click')
    await flushPromises()
    expect(api.auth.removePasskey).not.toHaveBeenCalled()
  })

  it('removes the app on the password, naming the passkeys that go with it', async () => {
    const wrapper = await open(mfa({ passkeys: [passkey('a1', 'Laptop')] }))
    await button(group(wrapper, 'Authenticator app'), 'Remove').trigger('click')
    expect(dialog(wrapper).get('h2').text()).toBe('Remove the authenticator app')
    expect(dialog(wrapper).get('li').text()).toBe('Laptop')
    await wrapper.get('#remove-app-password').setValue('pw pw pw pw pw')
    vi.mocked(api.auth.mfaClear).mockResolvedValue(undefined)
    vi.mocked(api.auth.mfa).mockResolvedValue(mfa({ totp: null }))

    await wrapper.get('#remove-app-form').trigger('submit')
    await flushPromises()

    expect(api.auth.mfaClear).toHaveBeenCalledWith('pw pw pw pw pw')
    expect(badge(wrapper)).toBe('off')
    expect(dialog(wrapper).exists()).toBe(false)
    expect(api.auth.me).toHaveBeenCalled()
  })

  it('keeps the app when the password is wrong', async () => {
    const wrapper = await open(mfa())
    await button(group(wrapper, 'Authenticator app'), 'Remove').trigger('click')
    await wrapper.get('#remove-app-password').setValue('nope')
    vi.mocked(api.auth.mfaClear).mockRejectedValue(new ApiError(401, 'password is wrong'))

    await wrapper.get('#remove-app-form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toBe('Password is wrong.')
    expect(badge(wrapper)).toBe('on')
  })

  it('works the same for a viewer, whose own sign-in is theirs to change', async () => {
    const wrapper = await open(mfa({ totp: null }), 'viewer')
    await button(wrapper, 'Set up authenticator app').trigger('click')
    await flushPromises()
    expect(api.auth.totpStart).toHaveBeenCalledOnce()
  })

  it('tells a locked-out account where to turn', async () => {
    const wrapper = await open(mfa())
    expect(wrapper.text()).toContain('Locked out? Run ostiole mfa bypass on the router.')
  })
})
