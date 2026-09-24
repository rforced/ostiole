import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import TokensSection from '@/views/system/TokensSection.vue'

vi.mock('@/lib/api', () => ({
  api: { tokens: { list: vi.fn(), create: vi.fn(), remove: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  ConfirmButton: true,
  TokenDialog: { template: '<div />' },
}

/** Mounts the card with a token just minted, the one moment its secret is shown. */
async function minted() {
  api.tokens.list.mockResolvedValue([])
  api.tokens.create.mockResolvedValue({ name: 'ci', secret: 'ost_secret' })
  const wrapper = mount(TokensSection, { global: { stubs }, attachTo: document.body })
  await flushPromises()
  wrapper.findComponent(stubs.TokenDialog).vm.$emit('create', { name: 'ci' })
  await flushPromises()
  return wrapper
}

const copyButton = (w) => w.findAll('button').find((b) => /Cop|Selected/.test(b.text()))

describe('TokensSection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('copies the secret with the clipboard where the page may use it', async () => {
    const writeText = vi.fn().mockResolvedValue()
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
    document.execCommand = vi.fn().mockReturnValue(true)
    const wrapper = await minted()
    await copyButton(wrapper).trigger('click')
    await flushPromises()
    expect(document.execCommand).toHaveBeenCalledWith('copy')
    expect(window.getSelection().toString()).toBe('ost_secret')
    expect(copyButton(wrapper).text()).toBe('Copied')
  })
})
