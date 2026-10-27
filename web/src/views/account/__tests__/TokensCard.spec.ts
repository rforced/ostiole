import { type VueWrapper, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { type Token, api } from '@/lib/api'
import TokensCard from '@/views/account/TokensCard.vue'
import TokenDialog from '@/views/system/accounts/TokenDialog.vue'
import { fixture } from '@/test/fixtures'

vi.mock('@/lib/api', () => ({
  api: { auth: { tokens: { list: vi.fn(), create: vi.fn(), remove: vi.fn() } } },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  ConfirmButton: { props: ['typed'], template: '<button type="button">Delete</button>' },
  TokenDialog: { template: '<div />' },
}

/** Mounts the card with a token just minted, the one moment its secret is shown. */
async function minted() {
  vi.mocked(api.auth.tokens.list).mockResolvedValue([])
  vi.mocked(api.auth.tokens.create).mockResolvedValue(
    fixture<Token & { secret: string }>({ name: 'ci', secret: 'ost_secret' }),
  )
  const wrapper = mount(TokensCard, { global: { stubs }, attachTo: document.body })
  await flushPromises()
  wrapper.findComponent(TokenDialog).vm.$emit('create', { name: 'ci' })
  await flushPromises()
  return wrapper
}

const copyButton = (w: VueWrapper) => {
  const found = w.findAll('button').find((b) => /Cop|Selected/.test(b.text()))
  if (!found) throw new Error('no copy button')
  return found
}

describe('TokensCard', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('copies the secret with the clipboard where the page may use it', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('isSecureContext', true)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const wrapper = await minted()
    await copyButton(wrapper).trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith('ost_secret')
    expect(copyButton(wrapper).text()).toBe('Copied')
  })

  // Plain HTTP on the LAN has no clipboard API; the old copy command does it.
  it('falls back to the copy command over plain HTTP', async () => {
    vi.stubGlobal('isSecureContext', false)
    const exec = vi.fn().mockReturnValue(true)
    document.execCommand = exec
    const wrapper = await minted()
    await copyButton(wrapper).trigger('click')
    await flushPromises()
    expect(exec).toHaveBeenCalledWith('copy')
    expect(window.getSelection()?.toString()).toBe('ost_secret')
    expect(copyButton(wrapper).text()).toBe('Copied')
  })

  it("lists and deletes the account's own tokens", async () => {
    vi.mocked(api.auth.tokens.list).mockResolvedValue([
      fixture<Token>({
        id: 'tk1',
        name: 'grafite',
        role: 'viewer',
        createdAt: '2026-10-01T00:00:00Z',
      }),
    ])
    vi.mocked(api.auth.tokens.remove).mockResolvedValue(undefined)
    const wrapper = mount(TokensCard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('grafite')
    expect(wrapper.findAll('button').some((b) => b.text() === 'Add token')).toBe(true)
    const del = wrapper.findComponent(ConfirmButton)
    expect(del.props('typed')).toBe('grafite')
    del.vm.$emit('confirm')
    await flushPromises()
    expect(api.auth.tokens.remove).toHaveBeenCalledWith('tk1')
    expect(api.auth.tokens.list).toHaveBeenCalledTimes(2)
  })
})
