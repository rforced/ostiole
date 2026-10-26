import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError, api } from '@/lib/api'
import TotpDialog from '@/views/account/TotpDialog.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {
    status: number
    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  },
  api: { auth: { totpStart: vi.fn(), totpConfirm: vi.fn() } },
}))

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template:
      '<div><h2>{{ title }}</h2><p>{{ description }}</p><slot /><slot name="footer" /></div>',
  },
}

const SECRET = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'
const URI = `otpauth://totp/Ostiole:sam?secret=${SECRET}&issuer=Ostiole&algorithm=SHA1&digits=6&period=30`

async function open(props = {}) {
  vi.mocked(api.auth.totpStart).mockResolvedValue({ secret: SECRET, uri: URI })
  const wrapper = mount(TotpDialog, { props: { open: true, ...props }, global: { stubs } })
  await flushPromises()
  return wrapper
}

const confirmButton = (wrapper) => wrapper.findAll('button').find((b) => b.text() === 'Confirm')

describe('TotpDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('shows the code to scan and the secret to type', async () => {
    const wrapper = await open()
    expect(api.auth.totpStart).toHaveBeenCalledOnce()
    const qr = wrapper.get('svg[role="img"]')
    expect(qr.get('path').attributes('d')).toMatch(/^M\d+ \d+h1v1h-1z/)
    expect(qr.get('path').attributes('fill')).toBe('#000')
    expect(wrapper.get<HTMLInputElement>('#totp-secret').element.value).toBe(SECRET)
    expect(wrapper.text()).toContain(
      'Scan the code, or enter the secret by hand in the app, then type the code it shows.',
    )
  })

  it('confirms with the code the app shows, spaces dropped', async () => {
    const wrapper = await open()
    expect(confirmButton(wrapper).attributes('disabled')).toBeDefined()
    await wrapper.get('#totp-code').setValue('123 45')
    expect(confirmButton(wrapper).attributes('disabled')).toBeDefined()
    await wrapper.get('#totp-code').setValue('123 456')
    expect(confirmButton(wrapper).attributes('disabled')).toBeUndefined()
    vi.mocked(api.auth.totpConfirm).mockResolvedValue(undefined)

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(api.auth.totpConfirm).toHaveBeenCalledWith('123456')
    expect(wrapper.emitted('done')).toHaveLength(1)
    expect(wrapper.emitted('update:open')[0]).toEqual([false])
  })

  it('stays open with the reason when the code is wrong', async () => {
    const wrapper = await open()
    await wrapper.get('#totp-code').setValue('000000')
    vi.mocked(api.auth.totpConfirm).mockRejectedValue(new ApiError(400, 'invalid code'))

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toBe('Invalid code.')
    expect(wrapper.emitted('done')).toBeUndefined()
    expect(wrapper.emitted('update:open')).toBeUndefined()
  })

  it('says it replaces the app when one is there already', async () => {
    const wrapper = await open({ replacing: true })
    expect(wrapper.get('h2').text()).toBe('Replace authenticator app')
    expect(wrapper.text()).toContain('The current app stops working once you confirm.')
  })

  it('shows no code to scan when the router cannot make one', async () => {
    vi.mocked(api.auth.totpStart).mockRejectedValue(new Error('authentication not available'))
    const wrapper = mount(TotpDialog, { props: { open: true }, global: { stubs } })
    await flushPromises()
    expect(wrapper.find('svg[role="img"]').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toBe('authentication not available')
    expect(confirmButton(wrapper).attributes('disabled')).toBeDefined()
  })
})
