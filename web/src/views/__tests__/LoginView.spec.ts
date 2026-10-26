import { flushPromises, mount } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import { ApiError, api } from '@/lib/api'
import type { AssertionResponse } from '@/lib/types.gen'
import * as webauthn from '@/lib/webauthn'
import { fixture, must, sessionOf } from '@/test/fixtures'
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
  ApiError: class ApiError extends Error {
    status: number
    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  },
}))
vi.mock('@/lib/webauthn', () => ({ available: vi.fn(), get: vi.fn() }))

const session = sessionOf({
  username: 'sam',
  role: 'admin',
  created: '2026-09-19T12:00:00Z',
  lastSeen: '2026-09-19T12:00:00Z',
  expires: '2026-09-20T12:00:00Z',
})
const credential = fixture<AssertionResponse>({
  id: 'cred',
  rawId: '',
  type: 'public-key',
  response: { clientDataJSON: '' },
})

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
const subtitle = (wrapper: VueWrapper) => wrapper.get('h1 + p')

const button = (wrapper: VueWrapper, text: string) =>
  wrapper.findAll('button').find((b) => b.text() === text)

/** Gives the username and goes on to the password. */
async function continueAs(wrapper: VueWrapper, name = 'sam') {
  await wrapper.get('#username').setValue(name)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

async function typePassword(wrapper: VueWrapper, pw: string) {
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
    vi.mocked(api.auth.loginPage).mockResolvedValue({ hostname: 'edge' })
    const { wrapper } = await page()
    expect(subtitle(wrapper).text()).toBe('edge')
    expect(subtitle(wrapper).get('span').classes()).toContain('font-mono')
  })

  it('says Ostiole firewall when the router has no hostname', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({})
    const { wrapper } = await page()
    expect(subtitle(wrapper).text()).toBe('Ostiole firewall')
    expect(subtitle(wrapper).get('span').classes()).not.toContain('invisible')
  })

  it('says Ostiole firewall when the router cannot be asked', async () => {
    vi.mocked(api.auth.loginPage).mockRejectedValue(new Error('offline'))
    const { wrapper } = await page()
    expect(subtitle(wrapper).text()).toBe('Ostiole firewall')
    expect(subtitle(wrapper).get('span').classes()).not.toContain('invisible')
  })

  it('goes where it was sent only on this site', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({})
    vi.mocked(api.auth.login).mockResolvedValue(session)
    for (const [redirect, want] of [
      ['/firewall/rules', '/firewall/rules'],
      ['//evil.example/x', '/'],
      ['/\\evil.example/x', '/'],
      ['https://evil.example/x', '/'],
    ] satisfies [string, string][]) {
      const { wrapper, router } = await page('/login?redirect=' + encodeURIComponent(redirect))
      await continueAs(wrapper)
      await typePassword(wrapper, 'secret')
      expect(router.currentRoute.value.fullPath, redirect).toBe(want)
    }
  })

  it('holds the line invisible while it asks', async () => {
    vi.mocked(api.auth.loginPage).mockReturnValue(new Promise(() => {}))
    const { wrapper } = await page()
    expect(subtitle(wrapper).get('span').classes()).toContain('invisible')
  })
})

describe('LoginView second step', () => {
  const due = (extra = {}) => ({
    pending: { token: 'tok', totp: true, passkeys: false, rpId: '', ...extra },
  })
  const refused = (status: number, message: string) => new ApiError(status, message)

  /** Passes the password step and stops at the second one. */
  async function afterPassword(answer = due(), path = '/login', options = {}) {
    setActivePinia(createPinia())
    vi.mocked(api.auth.loginPage).mockResolvedValue({})
    vi.mocked(api.auth.login).mockResolvedValue(answer)
    const out = await page(path, options)
    await continueAs(out.wrapper)
    await typePassword(out.wrapper, 'hunter2 hunter2')
    return out
  }

  const typeCode = async (wrapper: VueWrapper, code: string) => {
    await wrapper.get('#code').setValue(code)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
  }

  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.mocked(webauthn.available).mockReturnValue(true)
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
    vi.mocked(api.auth.loginTOTP).mockResolvedValue(session)
    await typeCode(wrapper, '123 456')
    expect(api.auth.loginTOTP).toHaveBeenCalledWith('tok', '123456')
    expect(router.currentRoute.value.fullPath).toBe('/firewall/rules')
  })

  it('keeps the step open and says why a code was refused', async () => {
    const { wrapper, router } = await afterPassword()
    const cases: [Error, string][] = [
      [refused(401, 'invalid code'), 'Invalid code.'],
      [refused(401, 'that code was used already'), 'That code was used already.'],
      [refused(429, 'too many failed logins; try again later'), 'Too many failed attempts.'],
      [refused(401, 'authentication required'), 'This sign-in has ended.'],
    ]
    for (const [error, text] of cases) {
      vi.mocked(api.auth.loginTOTP).mockRejectedValueOnce(error)
      await typeCode(wrapper, '000000')
      expect(wrapper.get('[role="alert"]').text(), text).toContain(text)
      expect(wrapper.find('#code').exists()).toBe(true)
      expect(router.currentRoute.value.path).toBe('/login')
    }
  })

  it('puts the passkey first, with the focus, when the account has one here', async () => {
    const { wrapper } = await afterPassword(
      due({ passkeys: true, rpId: 'edge.example' }),
      '/login',
      { attachTo: document.body },
    )
    const passkey = must(button(wrapper, 'Use a passkey'))
    expect(document.activeElement).toBe(passkey.element)
    expect(passkey.classes()).toContain('btn-primary')
    expect(must(button(wrapper, 'Sign in')).classes()).toContain('btn-secondary')
    const order = wrapper.findAll('button, input').map((e) => e.attributes('id') ?? e.text())
    expect(order.indexOf('Use a passkey')).toBeLessThan(order.indexOf('code'))
    expect(order.indexOf('code')).toBeLessThan(order.indexOf('Sign in'))
    wrapper.unmount()
  })

  it('goes back to the password with the username kept and nothing sent', async () => {
    const { wrapper } = await afterPassword()
    await must(button(wrapper, 'Back')).trigger('click')
    expect(wrapper.find('#code').exists()).toBe(false)
    expect(wrapper.find('#username').exists()).toBe(false)
    expect(wrapper.text()).toContain('sam')
    expect(wrapper.get<HTMLInputElement>('#password').element.value).toBe('')
    expect(webauthn.get).not.toHaveBeenCalled()
    expect(api.auth.loginTOTP).not.toHaveBeenCalled()
  })

  it('offers a passkey only when the account has one and the browser can use it', async () => {
    const offered = (wrapper: VueWrapper) => button(wrapper, 'Use a passkey')
    let { wrapper } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' }))
    expect(offered(wrapper)).toBeDefined()
    ;({ wrapper } = await afterPassword(due({ passkeys: false })))
    expect(offered(wrapper)).toBeUndefined()
    vi.mocked(webauthn.available).mockReturnValue(false)
    ;({ wrapper } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' })))
    expect(offered(wrapper)).toBeUndefined()
  })

  it('signs in with a passkey', async () => {
    const { wrapper, router } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' }))
    vi.mocked(api.auth.passkeyLoginOptions).mockResolvedValue({ publicKey: { challenge: 'c' } })
    vi.mocked(webauthn.get).mockResolvedValue(credential)
    vi.mocked(api.auth.loginPasskey).mockResolvedValue(session)
    await must(button(wrapper, 'Use a passkey')).trigger('click')
    await flushPromises()
    expect(api.auth.passkeyLoginOptions).toHaveBeenCalledWith('tok')
    expect(webauthn.get).toHaveBeenCalledWith({ challenge: 'c' }, { signal: undefined })
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('tok', credential)
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('says a cancelled passkey prompt and stays on the step', async () => {
    const { wrapper, router } = await afterPassword(due({ passkeys: true, rpId: 'edge.example' }))
    vi.mocked(api.auth.passkeyLoginOptions).mockResolvedValue({ publicKey: { challenge: 'c' } })
    vi.mocked(webauthn.get).mockRejectedValue(
      new Error('The passkey prompt was cancelled or timed out.'),
    )
    await must(button(wrapper, 'Use a passkey')).trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'The passkey prompt was cancelled or timed out.',
    )
    expect(wrapper.find('#code').exists()).toBe(true)
    expect(router.currentRoute.value.path).toBe('/login')
  })
})

describe('LoginView steps', () => {
  const offer = { token: 'pk', publicKey: { challenge: 'c' } }
  const refused = (status: number, message: string) => new ApiError(status, message)
  const named = (name: string, message: string) => Object.assign(new Error(message), { name })
  /** A prompt that stays open until its signal aborts it. */
  const openPrompt = () =>
    vi
      .mocked(webauthn.get)
      .mockImplementation(
        (_, args) =>
          new Promise((_, reject) =>
            must(must(args).signal).addEventListener('abort', () =>
              reject(named('AbortError', 'The passkey prompt was cancelled.')),
            ),
          ),
      )
  /** The signal the open prompt was given. */
  const openedSignal = () => must(must(vi.mocked(webauthn.get).mock.calls[0]?.[1]).signal)
  const ALONE = 'Sign in with a passkey'

  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.mocked(api.auth.loginPage).mockResolvedValue({})
    vi.mocked(api.auth.loginStart).mockResolvedValue(offer)
    vi.mocked(webauthn.available).mockReturnValue(true)
  })

  it('asks the username first, then the password with the username kept as a line', async () => {
    const { wrapper } = await page('/login', { attachTo: document.body })
    expect(wrapper.find('#password').exists()).toBe(false)
    expect(button(wrapper, 'Continue')).toBeDefined()
    await continueAs(wrapper, '  sam ')
    expect(wrapper.find('#username').exists()).toBe(false)
    expect(wrapper.text()).toContain('sam')
    expect(button(wrapper, 'Change')).toBeDefined()
    expect(button(wrapper, 'Sign in')).toBeDefined()
    expect(button(wrapper, 'Use a passkey')).toBeUndefined()
    expect(document.activeElement).toBe(wrapper.get('#password').element)
    expect(api.auth.loginStart).not.toHaveBeenCalled()
    expect(webauthn.get).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('signs in with the password on the second step', async () => {
    vi.mocked(api.auth.login).mockResolvedValue(session)
    const { wrapper, router } = await page()
    await continueAs(wrapper)
    await typePassword(wrapper, 'secret')
    expect(api.auth.login).toHaveBeenCalledWith('sam', 'secret')
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('says a wrong password and stays on the step', async () => {
    vi.mocked(api.auth.login).mockRejectedValue(refused(401, 'invalid credentials'))
    const { wrapper, router } = await page()
    await continueAs(wrapper)
    await typePassword(wrapper, 'wrong')
    expect(wrapper.get('[role="alert"]').text()).toBe('Invalid username or password.')
    expect(wrapper.get<HTMLInputElement>('#password').element.value).toBe('')
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('offers a passkey before the name only where the router has one and the browser can use it', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    let { wrapper } = await page()
    expect(button(wrapper, ALONE)).toBeDefined()
    await continueAs(wrapper)
    expect(button(wrapper, 'Use a passkey')).toBeDefined()
    expect(webauthn.get).not.toHaveBeenCalled()

    vi.mocked(api.auth.loginPage).mockResolvedValue({})
    ;({ wrapper } = await page())
    expect(button(wrapper, ALONE)).toBeUndefined()
    await continueAs(wrapper)
    expect(button(wrapper, 'Use a passkey')).toBeUndefined()

    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    vi.mocked(webauthn.available).mockReturnValue(false)
    ;({ wrapper } = await page())
    expect(button(wrapper, ALONE)).toBeUndefined()
    await continueAs(wrapper)
    expect(button(wrapper, 'Use a passkey')).toBeUndefined()
  })

  it('signs in with a passkey and nothing typed', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    vi.mocked(webauthn.get).mockResolvedValue(credential)
    vi.mocked(api.auth.loginPasskey).mockResolvedValue(session)
    const { wrapper, router } = await page('/login?redirect=%2Ffirewall%2Frules')
    await must(button(wrapper, ALONE)).trigger('click')
    await flushPromises()
    expect(api.auth.loginStart).toHaveBeenCalledWith()
    expect(webauthn.get).toHaveBeenCalledWith(
      { challenge: 'c' },
      { signal: expect.any(AbortSignal) },
    )
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('pk', credential)
    expect(api.auth.login).not.toHaveBeenCalled()
    expect(router.currentRoute.value.fullPath).toBe('/firewall/rules')
  })

  it('signs in with a passkey from the password step, whatever name was typed', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    vi.mocked(webauthn.get).mockResolvedValue(credential)
    vi.mocked(api.auth.loginPasskey).mockResolvedValue(session)
    const { wrapper, router } = await page()
    await continueAs(wrapper, 'someone')
    await must(button(wrapper, 'Use a passkey')).trigger('click')
    await flushPromises()
    expect(api.auth.loginStart).toHaveBeenCalledWith()
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('pk', credential)
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('says nothing when the prompt is cancelled, and starts afresh on the next try', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    vi.mocked(webauthn.get).mockRejectedValueOnce(
      named('NotAllowedError', 'The passkey prompt was cancelled or timed out.'),
    )
    const { wrapper, router } = await page()
    await must(button(wrapper, ALONE)).trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(api.auth.loginPasskey).not.toHaveBeenCalled()
    expect(router.currentRoute.value.path).toBe('/login')

    vi.mocked(webauthn.get).mockResolvedValue(credential)
    vi.mocked(api.auth.loginPasskey).mockResolvedValue(session)
    await must(button(wrapper, ALONE)).trigger('click')
    await flushPromises()
    expect(api.auth.loginStart).toHaveBeenCalledTimes(2)
    expect(api.auth.loginPasskey).toHaveBeenCalledWith('pk', credential)
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('says why the router refused the passkey', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    vi.mocked(webauthn.get).mockResolvedValue(credential)
    const { wrapper, router } = await page()
    const cases: [Error, string][] = [
      [refused(401, 'no such passkey'), 'That passkey is not registered here.'],
      [
        refused(401, 'passkey refused: the device did not check that it was you'),
        'The passkey was refused.',
      ],
      [
        refused(400, 'that passkey request timed out; try again'),
        'The passkey request timed out. Try again.',
      ],
      [refused(401, 'authentication required'), 'That sign-in has ended. Try again.'],
      [
        refused(429, 'too many failed logins'),
        'Too many failed attempts. Try again in a few minutes.',
      ],
    ]
    for (const [error, text] of cases) {
      vi.mocked(api.auth.loginPasskey).mockRejectedValueOnce(error)
      await must(button(wrapper, ALONE)).trigger('click')
      await flushPromises()
      expect(wrapper.get('[role="alert"]').text(), text).toBe(text)
      expect(wrapper.find('#username').exists()).toBe(true)
      expect(router.currentRoute.value.path).toBe('/login')
    }
  })

  it('says when the router shuts the address out before the prompt', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    vi.mocked(api.auth.loginStart).mockRejectedValue(refused(429, 'too many failed logins'))
    const { wrapper } = await page()
    await must(button(wrapper, ALONE)).trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'Too many failed attempts. Try again in a few minutes.',
    )
    expect(webauthn.get).not.toHaveBeenCalled()
  })

  it('closes a prompt still open on Continue', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    openPrompt()
    const { wrapper } = await page()
    await must(button(wrapper, ALONE)).trigger('click')
    await flushPromises()
    expect(button(wrapper, 'Waiting…')).toBeDefined()
    const signal = openedSignal()
    await continueAs(wrapper)
    expect(signal.aborted).toBe(true)
    expect(wrapper.find('#password').exists()).toBe(true)
    expect(must(button(wrapper, 'Use a passkey')).attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })

  it('closes a prompt still open when the password is sent', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    openPrompt()
    vi.mocked(api.auth.login).mockResolvedValue(session)
    const { wrapper, router } = await page()
    await continueAs(wrapper)
    await must(button(wrapper, 'Use a passkey')).trigger('click')
    await flushPromises()
    const signal = openedSignal()
    await typePassword(wrapper, 'secret')
    expect(signal.aborted).toBe(true)
    expect(api.auth.login).toHaveBeenCalledWith('sam', 'secret')
    expect(api.auth.loginPasskey).not.toHaveBeenCalled()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('goes back to the username on Change, closing an open prompt', async () => {
    vi.mocked(api.auth.loginPage).mockResolvedValue({ passkeys: true })
    openPrompt()
    const { wrapper } = await page('/login', { attachTo: document.body })
    await continueAs(wrapper)
    await must(button(wrapper, 'Use a passkey')).trigger('click')
    await flushPromises()
    const signal = openedSignal()
    await must(button(wrapper, 'Change')).trigger('click')
    await flushPromises()
    expect(signal.aborted).toBe(true)
    expect(wrapper.find('#password').exists()).toBe(false)
    expect(wrapper.get<HTMLInputElement>('#username').element.value).toBe('sam')
    expect(document.activeElement).toBe(wrapper.get('#username').element)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
