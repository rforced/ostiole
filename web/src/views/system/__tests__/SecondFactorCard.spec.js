import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'

import { ApiError, api } from '@/lib/api'
import * as webauthn from '@/lib/webauthn'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'
import SecondFactorCard from '@/views/system/accounts/SecondFactorCard.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: {
    auth: {
      secondFactor: vi.fn(),
      removePasskey: vi.fn(),
      secondFactorClear: vi.fn(),
      me: vi.fn(),
      totpStart: vi.fn(),
    },
  },
}))
vi.mock('@/lib/webauthn', () => ({ available: vi.fn() }))

// The dialogs live in a portal, which mount() cannot reach; open them in place.
const AppDialogStub = {
  props: ['open', 'title', 'description'],
  template: '<div v-if="open"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>',
}

const passkey = (id, name) => ({
  id,
  name,
  rpId: 'edge.example',
  createdAt: '2026-10-01T10:00:00Z',
  lastUsed: '2026-10-05T10:00:00Z',
})

async function open(state, role = 'viewer') {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  webauthn.available.mockReturnValue(true)
  api.auth.secondFactor.mockResolvedValue(state)
  api.auth.me.mockResolvedValue({ username: 'sam', role })
  useAuthStore().user = { username: 'sam', role }
  const wrapper = mount(SecondFactorCard, { global: { stubs: { AppDialog: AppDialogStub } } })
  await flushPromises()
  return wrapper
}

const button = (scope, text) => scope.findAll('button').find((b) => b.text() === text)
const disabled = (b) => b.attributes('disabled') !== undefined

describe('SecondFactorCard', () => {
  it('starts with the authenticator app, and says why a passkey has to wait', async () => {
    const wrapper = await open({ totp: false, passkeys: [], rpId: 'edge.example' })
    expect(wrapper.text()).toContain('Off. Sign-in asks for your password alone.')
    expect(disabled(button(wrapper, 'Set up authenticator app'))).toBe(false)
    expect(disabled(button(wrapper, 'Add a passkey'))).toBe(true)
    expect(wrapper.text()).toContain('Passkeys need an authenticator app first.')
    expect(button(wrapper, 'Turn off')).toBeUndefined()
    expect(wrapper.find('table').exists()).toBe(false)
  })

  it('will not make a passkey for an address that is not a name', async () => {
    const wrapper = await open({ totp: true, passkeys: [], rpId: '' })
    expect(disabled(button(wrapper, 'Add a passkey'))).toBe(true)
    expect(wrapper.text()).toContain('Passkeys need a name in the address bar, not an address.')
  })

  it('adds a passkey once the app is set up and the page has a name', async () => {
    const wrapper = await open({ totp: true, passkeys: [], rpId: 'edge.example' })
    expect(disabled(button(wrapper, 'Add a passkey'))).toBe(false)
    expect(wrapper.text()).toContain('No passkeys.')
    await button(wrapper, 'Add a passkey').trigger('click')
    expect(wrapper.find('#passkey-name').exists()).toBe(true)
  })

  it('removes a passkey once the confirm says yes, and lists what is left', async () => {
    const wrapper = await open({
      totp: true,
      passkeys: [passkey('a1', 'Laptop'), passkey('b2', 'Phone')],
      rpId: 'edge.example',
    })
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    api.auth.removePasskey.mockResolvedValue(undefined)
    api.auth.secondFactor.mockResolvedValue({
      totp: true,
      passkeys: [passkey('b2', 'Phone')],
      rpId: 'edge.example',
    })
    const rows = () => wrapper.findAll('tbody tr')
    expect(rows().map((r) => r.get('td').text())).toEqual(['Laptop', 'Phone'])

    await button(rows()[0], 'Remove').trigger('click')
    await flushPromises()

    expect(ask).toHaveBeenCalledWith(
      expect.objectContaining({ question: 'Remove the passkey Laptop?' }),
    )
    expect(api.auth.removePasskey).toHaveBeenCalledWith('a1')
    expect(rows().map((r) => r.get('td').text())).toEqual(['Phone'])
  })

  it('keeps the passkey when the confirm is declined', async () => {
    const wrapper = await open({
      totp: true,
      passkeys: [passkey('a1', 'Laptop')],
      rpId: 'edge.example',
    })
    vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(false)
    await button(wrapper.get('tbody tr'), 'Remove').trigger('click')
    await flushPromises()
    expect(api.auth.removePasskey).not.toHaveBeenCalled()
  })

  it('turns the second factor off on the password, and shows it off', async () => {
    const wrapper = await open({
      totp: true,
      passkeys: [passkey('a1', 'Laptop')],
      rpId: 'e.example',
    })
    await button(wrapper, 'Turn off').trigger('click')
    await wrapper.get('#second-factor-password').setValue('pw pw pw pw pw')
    api.auth.secondFactorClear.mockResolvedValue(undefined)
    api.auth.secondFactor.mockResolvedValue({ totp: false, passkeys: [], rpId: 'e.example' })

    await wrapper.get('#second-factor-off-form').trigger('submit')
    await flushPromises()

    expect(api.auth.secondFactorClear).toHaveBeenCalledWith('pw pw pw pw pw')
    expect(wrapper.text()).toContain('Off. Sign-in asks for your password alone.')
    expect(wrapper.find('#second-factor-password').exists()).toBe(false)
  })

  it('keeps it on when the password is wrong', async () => {
    const wrapper = await open({ totp: true, passkeys: [], rpId: 'e.example' })
    await button(wrapper, 'Turn off').trigger('click')
    await wrapper.get('#second-factor-password').setValue('nope')
    api.auth.secondFactorClear.mockRejectedValue(
      Object.assign(new ApiError('invalid username or password'), { status: 401 }),
    )

    await wrapper.get('#second-factor-off-form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toBe('Password is wrong.')
    expect(wrapper.text()).toContain('On. Sign-in asks for a code from your authenticator app.')
  })

  it('works the same for a viewer, whose own sign-in is theirs to change', async () => {
    const wrapper = await open({ totp: false, passkeys: [], rpId: 'e.example' }, 'viewer')
    await button(wrapper, 'Set up authenticator app').trigger('click')
    await flushPromises()
    expect(api.auth.totpStart).toHaveBeenCalledOnce()
  })

  it('says when the command line has bypassed it', async () => {
    const wrapper = await open({
      totp: true,
      passkeys: [],
      rpId: 'e.example',
      bypassUntil: '2099-10-10T12:00:00Z',
      bypassBy: 'root',
    })
    const note = wrapper.get('[role="note"]').text()
    expect(note).toMatch(
      /^The second factor is bypassed until .+ by root, from the command line\.$/,
    )
  })
})
