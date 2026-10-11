import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import * as webauthn from '@/lib/webauthn'
import PasskeyDialog from '@/views/system/accounts/PasskeyDialog.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: { auth: { passkeyOptions: vi.fn(), addPasskey: vi.fn() } },
}))
vi.mock('@/lib/webauthn', () => ({ create: vi.fn() }))

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><p>{{ description }}</p><slot /><slot name="footer" /></div>',
  },
}

function open() {
  return mount(PasskeyDialog, { props: { open: true, rpId: 'edge.example' }, global: { stubs } })
}

const addButton = (wrapper) => wrapper.findAll('button').find((b) => b.text() === 'Add passkey')

describe('PasskeyDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('asks for a name, then the browser, then the router', async () => {
    const wrapper = open()
    expect(wrapper.text()).toContain('Made for edge.example. It signs in only there.')
    expect(addButton(wrapper).attributes('disabled')).toBeDefined()
    await wrapper.get('#passkey-name').setValue('  Laptop ')
    api.auth.passkeyOptions.mockResolvedValue({ publicKey: { challenge: 'c' } })
    webauthn.create.mockResolvedValue({ id: 'cred' })
    api.auth.addPasskey.mockResolvedValue(undefined)

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(webauthn.create).toHaveBeenCalledWith({ challenge: 'c' })
    expect(api.auth.addPasskey).toHaveBeenCalledWith('Laptop', { id: 'cred' })
    expect(wrapper.emitted('done')).toHaveLength(1)
    expect(wrapper.emitted('update:open')[0]).toEqual([false])
  })

  it('says a cancelled prompt in a sentence and adds nothing', async () => {
    const wrapper = open()
    await wrapper.get('#passkey-name').setValue('Laptop')
    api.auth.passkeyOptions.mockResolvedValue({ publicKey: {} })
    webauthn.create.mockRejectedValue(new Error('The passkey prompt was cancelled or timed out.'))

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toBe(
      'The passkey prompt was cancelled or timed out.',
    )
    expect(api.auth.addPasskey).not.toHaveBeenCalled()
    expect(wrapper.emitted('done')).toBeUndefined()
  })

  it('shows what the router said when it will not take the passkey', async () => {
    const wrapper = open()
    await wrapper.get('#passkey-name').setValue('Laptop')
    api.auth.passkeyOptions.mockRejectedValue(
      new Error('set up an authenticator app before adding a passkey'),
    )

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('set up an authenticator app')
    expect(webauthn.create).not.toHaveBeenCalled()
  })
})
