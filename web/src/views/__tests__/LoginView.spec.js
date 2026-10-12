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
      loginStart: vi.fn(),
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

const button = (wrapper, text) => wrapper.findAll('button').find((b) => b.text() === text)

/** Gives the username and goes on to the password. */
async function continueAs(wrapper, name = 'sam') {
  await wrapper.get('#username').setValue(name)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

async function typePassword(wrapper, pw) {
  await wrapper.get('#password').setValue(pw)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

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
    api.auth.loginStart.mockResolvedValue({ passkey: null })
    api.auth.login.mockResolvedValue({ username: 'sam', role: 'admin' })
    for (const [redirect, want] of [
      ['/firewall/rules', '/firewall/rules'],
      ['//evil.example/x', '/'],
      ['/\\evil.example/x', '/'],
      ['https://evil.example/x', '/'],
    ]) {
      const { wrapper, router } = await page('/login?redirect=' + encodeURIComponent(redirect))
      await continueAs(wrapper)
      await typePassword(wrapper, 'secret')
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
    pending: { token: 'tok', totp: true, passkeys: false, rpId: '', ...extra },
  })
  const refused = (status, message) => Object.assign(new ApiError(message), { status })

  /** Passes the password step and stops at the second one. */
  async function afterPassword(answer = due(), path = '/login', options = {}) {
    setActivePinia(createPinia())
    api.auth.loginPage.mockResolvedValue({})
    api.auth.loginStart.mockResolvedValue({ passkey: null })
    api.auth.login.mockResolvedValue(answer)
    const out = await page(path, options)
    await continueAs(out.wrapper)
    await typePassword(out.wrapper, 'hunter2 hunter2')
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
    await button(wrapper, 'Back').trigger('click')
    expect(wrapper.find('#code').exists()).toBe(false)
    expect(wrapper.find('#username').exists()).toBe(false)
    expect(wrapper.text()).toContain('sam')
    expect(wrapper.get('#password').element.value).toBe('')
    expect(webauthn.get).not.toHaveBeenCalled()
    expect(api.auth.loginTOTP).not.toHaveBeenCalled()
  })

  it('offers a passkey only when the account has one and the browser can use it', async () => {
    const offered = (wrapper) => button(wrapper, 'Use a passkey')
    let { wrapper } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' }))
    expect(offered(wrapper)).toBeDefined()
    ;({ wrapper } = await afterPassword(due({ passkeys: false })))
    expect(offered(wrapper)).toBeUndefined()
    webauthn.available.mockReturnValue(false)
    ;({ wrapper } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' })))
    expect(offered(wrapper)).toBeUndefined()
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
    expect(webauthn.get).toHaveBeenCalledWith({ challenge: 'c' }, { signal: undefined })
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

describe('LoginView steps', () => {
  const session = { username: 'sam', role: 'admin' }
  const offer = { passkey: { token: 'pk', publicKey: { challenge: 'c' } } }
  const refused = (status, message) => Object.assign(new ApiError(message), { status })
  const named = (name, message) => Object.assign(new Error(message), { name })
  /** A prompt that stays open until its signal aborts it. */
  const openPrompt = () =>
    webauthn.get.mockImplementation(
      (_, { signal }) =>
        new Promise((_, reject) =>
          signal.addEventListener('abort', () =>
            reject(named('AbortError', 'The passkey prompt was cancelled.')),
          ),
        ),
    )

  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.auth.loginPage.mockResolvedValue({})
    api.auth.loginStart.mockResolvedValue({ passkey: null })
    webauthn.available.mockReturnValue(true)
  })

  it('asks the username first, then the password with the username kept as a line', async () => {
    const { wrapper } = await page('/login', { attachTo: document.body })
    expect(wrapper.find('#password').exists()).toBe(false)
    expect(button(wrapper, 'Continue')).toBeDefined()
    await continueAs(wrapper, '  sam ')
    expect(api.auth.loginStart).toHaveBeenCalledWith('sam')
    expect(wrapper.find('#username').exists()).toBe(false)
    expect(wrapper.text()).toContain('sam')
    expect(button(wrapper, 'Change')).toBeDefined()
    expect(button(wrapper, 'Sign in')).toBeDefined()
    expect(button(wrapper, 'Use a passkey')).toBeUndefined()
    expect(document.activeElement).toBe(wrapper.get('#password').element)
    expect(webauthn.get).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('signs in with the password on the second step', async () => {
    api.auth.login.mockResolvedValue(session)
    const { wrapper, router } = await page()
    await continueAs(wrapper)
    await typePassword(wrapper, 'secret')
    expect(api.auth.login).toHaveBeenCalledWith('sam', 'secret')
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('says a wrong password and stays on the step', async () => {
    api.auth.login.mockRejectedValue(refused(401, 'invalid credentials'))
    const { wrapper, router } = await page()
    await continueAs(wrapper)
    await typePassword(wrapper, 'wrong')
    expect(wrapper.get('[role="alert"]').text()).toBe('Invalid username or password.')
    expect(wrapper.get('#password').element.value).toBe('')
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('stays on the username when the router shuts the address out', async () => {
    api.auth.loginStart.mockRejectedValue(refused(429, 'too many failed logins'))
    const { wrapper } = await page()
    await continueAs(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'Too many failed attempts. Try again in a few minutes.',
    )
    expect(wrapper.find('#username').exists()).toBe(true)
    expect(wrapper.find('#password').exists()).toBe(false)
  })

  it('opens the passkey prompt at once and signs in with no password', async () => {
    api.auth.loginStart.mockResolvedValue(offer)
    webauthn.get.mockResolvedValue({ id: 'cred' })
    api.auth.loginPasskey.mockResolvedValue(session)
    const { wrapper, router } = await page('/login?redirect=%2Ffirewall%2Frules')
    await continueAs(wrapper)
    expect(webauthn.get).toHaveBeenCalledWith(
      { challenge: 'c' },
      { signal: expect.any(AbortSignal) },
    )
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('pk', { id: 'cred' })
    expect(api.auth.login).not.toHaveBeenCalled()
    expect(router.currentRoute.value.fullPath).toBe('/firewall/rules')
  })

  it('opens no prompt where the browser has no passkeys', async () => {
    api.auth.loginStart.mockResolvedValue(offer)
    webauthn.available.mockReturnValue(false)
    const { wrapper } = await page()
    await continueAs(wrapper)
    expect(webauthn.get).not.toHaveBeenCalled()
    expect(button(wrapper, 'Use a passkey')).toBeUndefined()
    expect(wrapper.find('#password').exists()).toBe(true)
  })

  it('says nothing when the prompt is cancelled, and offers it again', async () => {
    api.auth.loginStart.mockResolvedValue(offer)
    webauthn.get.mockRejectedValueOnce(
      named('NotAllowedError', 'The passkey prompt was cancelled or timed out.'),
    )
    const { wrapper, router } = await page()
    await continueAs(wrapper)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(api.auth.loginPasskey).not.toHaveBeenCalled()
    expect(router.currentRoute.value.path).toBe('/login')

    webauthn.get.mockResolvedValue({ id: 'cred' })
    api.auth.loginPasskey.mockResolvedValue(session)
    await button(wrapper, 'Use a passkey').trigger('click')
    await flushPromises()
    expect(webauthn.get).toHaveBeenCalledTimes(2)
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('pk', { id: 'cred' })
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('closes a prompt still open when the password is sent', async () => {
    api.auth.loginStart.mockResolvedValue(offer)
    openPrompt()
    api.auth.login.mockResolvedValue(session)
    const { wrapper, router } = await page()
    await continueAs(wrapper)
    expect(button(wrapper, 'Waiting…')).toBeDefined()
    const { signal } = webauthn.get.mock.calls[0][1]
    await typePassword(wrapper, 'secret')
    expect(signal.aborted).toBe(true)
    expect(api.auth.login).toHaveBeenCalledWith('sam', 'secret')
    expect(api.auth.loginPasskey).not.toHaveBeenCalled()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('says why the router refused the passkey and fetches a fresh one', async () => {
    api.auth.loginStart.mockResolvedValue(offer)
    webauthn.get.mockResolvedValue({ id: 'cred' })
    api.auth.loginPasskey.mockRejectedValueOnce(refused(401, 'passkey not recognised'))
    const { wrapper, router } = await page()
    await continueAs(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'The passkey was refused. Enter your password.',
    )
    expect(api.auth.loginStart).toHaveBeenCalledTimes(2)
    expect(button(wrapper, 'Use a passkey')).toBeDefined()
    expect(router.currentRoute.value.path).toBe('/login')

    api.auth.loginPasskey.mockRejectedValueOnce(refused(429, 'too many failed logins'))
    await button(wrapper, 'Use a passkey').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'Too many failed attempts. Try again in a few minutes.',
    )
  })

  it('goes back to the username on Change, closing an open prompt', async () => {
    api.auth.loginStart.mockResolvedValue(offer)
    openPrompt()
    const { wrapper } = await page('/login', { attachTo: document.body })
    await continueAs(wrapper)
    const { signal } = webauthn.get.mock.calls[0][1]
    await button(wrapper, 'Change').trigger('click')
    await flushPromises()
    expect(signal.aborted).toBe(true)
    expect(wrapper.find('#password').exists()).toBe(false)
    expect(wrapper.get('#username').element.value).toBe('sam')
    expect(document.activeElement).toBe(wrapper.get('#username').element)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
