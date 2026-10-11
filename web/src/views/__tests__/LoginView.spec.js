import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import { ApiError, api } from '@/lib/api'
import * as webauthn from '@/lib/webauthn'
import LoginView from '@/views/LoginView.vue'

vi.mock('@/lib/api', () => ({
  api: {
    auth: {
      loginPage: vi.fn(),
      login: vi.fn(),
      loginTOTP: vi.fn(),
      passkeyLoginOptions: vi.fn(),
      loginPasskey: vi.fn(),
    },
  },
  ApiError: class ApiError extends Error {},
}))
vi.mock('@/lib/webauthn', () => ({ available: vi.fn(), get: vi.fn() }))

async function page(path = '/login', options = {}) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:any(.*)*', component: LoginView }],
  })
  router.push(path)
  await router.isReady()
  const wrapper = mount(LoginView, {
    ...options,
    global: { plugins: [router], stubs: { ThemeToggle: true } },
  })
  await flushPromises()
  return { wrapper, router }
}

/** The line under the heading. */
const subtitle = (wrapper) => wrapper.get('h1 + p')

describe('LoginView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('names the router by its hostname', async () => {
    api.auth.loginPage.mockResolvedValue({ hostname: 'edge' })
    const { wrapper } = await page()
    expect(subtitle(wrapper).text()).toBe('edge')
    expect(subtitle(wrapper).get('span').classes()).toContain('font-mono')
  })

  it('says Ostiole firewall when the router has no hostname', async () => {
    api.auth.loginPage.mockResolvedValue({})
    const { wrapper } = await page()
    expect(subtitle(wrapper).text()).toBe('Ostiole firewall')
    expect(subtitle(wrapper).get('span').classes()).not.toContain('invisible')
  })

  it('says Ostiole firewall when the router cannot be asked', async () => {
    api.auth.loginPage.mockRejectedValue(new Error('offline'))
    const { wrapper } = await page()
    expect(subtitle(wrapper).text()).toBe('Ostiole firewall')
    expect(subtitle(wrapper).get('span').classes()).not.toContain('invisible')
  })

  it('goes where it was sent only on this site', async () => {
    api.auth.loginPage.mockResolvedValue({})
    api.auth.login.mockResolvedValue({ username: 'sam', role: 'admin' })
    for (const [redirect, want] of [
      ['/firewall/rules', '/firewall/rules'],
      ['//evil.example/x', '/'],
      ['/\\evil.example/x', '/'],
      ['https://evil.example/x', '/'],
    ]) {
      const { wrapper, router } = await page('/login?redirect=' + encodeURIComponent(redirect))
      await wrapper.get('#username').setValue('sam')
      await wrapper.get('#password').setValue('secret')
      await wrapper.get('form').trigger('submit')
      await flushPromises()
      expect(router.currentRoute.value.fullPath, redirect).toBe(want)
    }
  })

  it('holds the line invisible while it asks', async () => {
    api.auth.loginPage.mockReturnValue(new Promise(() => {}))
    const { wrapper } = await page()
    expect(subtitle(wrapper).get('span').classes()).toContain('invisible')
  })
})

describe('LoginView second step', () => {
  const session = { username: 'sam', role: 'admin' }
  const due = (extra = {}) => ({
    secondFactor: { pending: 'tok', totp: true, passkeys: false, rpId: '', ...extra },
  })
  const refused = (status, message) => Object.assign(new ApiError(message), { status })

  /** Passes the password step and stops at the second one. */
  async function afterPassword(answer = due(), path = '/login', options = {}) {
    setActivePinia(createPinia())
    api.auth.loginPage.mockResolvedValue({})
    api.auth.login.mockResolvedValue(answer)
    const out = await page(path, options)
    await out.wrapper.get('#username').setValue('sam')
    await out.wrapper.get('#password').setValue('hunter2 hunter2')
    await out.wrapper.get('form').trigger('submit')
    await flushPromises()
    return out
  }

  const typeCode = async (wrapper, code) => {
    await wrapper.get('#code').setValue(code)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
  }

  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    webauthn.available.mockReturnValue(true)
  })

  it('asks for a code, with the username and password gone from the page', async () => {
    const { wrapper, router } = await afterPassword(due(), '/login', { attachTo: document.body })
    const code = wrapper.get('#code')
    expect(wrapper.get('label[for="code"]').text()).toBe('Code from your authenticator app')
    expect(code.attributes('inputmode')).toBe('numeric')
    expect(code.attributes('autocomplete')).toBe('one-time-code')
    expect(document.activeElement).toBe(code.element)
    expect(wrapper.find('#username').exists()).toBe(false)
    expect(wrapper.find('#password').exists()).toBe(false)
    expect(wrapper.html()).not.toContain('hunter2')
    expect(router.currentRoute.value.path).toBe('/login')
    wrapper.unmount()
  })

  it('signs in with the code, spaces dropped, and goes where it was sent', async () => {
    const { wrapper, router } = await afterPassword(due(), '/login?redirect=%2Ffirewall%2Frules')
    api.auth.loginTOTP.mockResolvedValue(session)
    await typeCode(wrapper, '123 456')
    expect(api.auth.loginTOTP).toHaveBeenCalledWith('tok', '123456')
    expect(router.currentRoute.value.fullPath).toBe('/firewall/rules')
  })

  it('keeps the step open and says why a code was refused', async () => {
    const { wrapper, router } = await afterPassword()
    for (const [error, text] of [
      [refused(401, 'invalid code'), 'Invalid code.'],
      [refused(401, 'that code was used already'), 'That code was used already.'],
      [refused(429, 'too many failed logins; try again later'), 'Too many failed attempts.'],
      [refused(401, 'authentication required'), 'This sign-in has ended.'],
    ]) {
      api.auth.loginTOTP.mockRejectedValueOnce(error)
      await typeCode(wrapper, '000000')
      expect(wrapper.get('[role="alert"]').text(), text).toContain(text)
      expect(wrapper.find('#code').exists()).toBe(true)
      expect(router.currentRoute.value.path).toBe('/login')
    }
  })

  it('goes back to the password with the username kept and nothing sent', async () => {
    const { wrapper } = await afterPassword()
    const back = wrapper.findAll('button').find((b) => b.text() === 'Back')
    await back.trigger('click')
    expect(wrapper.find('#code').exists()).toBe(false)
    expect(wrapper.get('#username').element.value).toBe('sam')
    expect(wrapper.get('#password').element.value).toBe('')
    expect(api.auth.loginTOTP).not.toHaveBeenCalled()
  })

  it('offers a passkey only when the account has one and the browser can use it', async () => {
    const button = (wrapper) => wrapper.findAll('button').find((b) => b.text() === 'Use a passkey')
    let { wrapper } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' }))
    expect(button(wrapper)).toBeDefined()
    ;({ wrapper } = await afterPassword(due({ passkeys: false })))
    expect(button(wrapper)).toBeUndefined()
    webauthn.available.mockReturnValue(false)
    ;({ wrapper } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' })))
    expect(button(wrapper)).toBeUndefined()
  })

  it('signs in with a passkey', async () => {
    const { wrapper, router } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' }))
    api.auth.passkeyLoginOptions.mockResolvedValue({ publicKey: { challenge: 'c' } })
    webauthn.get.mockResolvedValue({ id: 'cred' })
    api.auth.loginPasskey.mockResolvedValue(session)
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Use a passkey')
      .trigger('click')
    await flushPromises()
    expect(api.auth.passkeyLoginOptions).toHaveBeenCalledWith('tok')
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('tok', { id: 'cred' })
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('says a cancelled passkey prompt and stays on the step', async () => {
    const { wrapper, router } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' }))
    api.auth.passkeyLoginOptions.mockResolvedValue({ publicKey: {} })
    webauthn.get.mockRejectedValue(new Error('The passkey prompt was cancelled or timed out.'))
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Use a passkey')
      .trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'The passkey prompt was cancelled or timed out.',
    )
    expect(wrapper.find('#code').exists()).toBe(true)
    expect(router.currentRoute.value.path).toBe('/login')
  })
})
