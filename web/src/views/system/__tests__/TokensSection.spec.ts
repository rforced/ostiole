import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import TokensSection from '@/views/system/TokensSection.vue'

vi.mock('@/lib/api', () => ({
  api: { tokens: { list: vi.fn(), remove: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = {
  ConfirmButton: { props: ['typed'], template: '<button type="button">Delete</button>' },
}

async function mounted() {
  vi.mocked(api.tokens.list).mockResolvedValue([
    {
      id: 'tk1',
      name: 'grafite',
      role: 'viewer',
      owner: 'marlow',
      createdAt: '2026-10-01T00:00:00Z',
    },
    { id: 'tk2', name: 'nightly', role: 'operator', createdAt: '2026-10-02T00:00:00Z' },
  ])
  const wrapper = mount(TokensSection, { global: { stubs } })
  await flushPromises()
  return wrapper
}

const cell = (w, row, label) => w.findAll('tbody tr')[row].find(`td[data-label="${label}"]`)

describe('TokensSection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('lists every token with its owner, a router token as router', async () => {
    const w = await mounted()
    expect(w.text()).toContain("Every account's tokens. New ones are made on the Account page.")
    expect(w.findAll('th').map((t) => t.text())).toEqual([
      'Token',
      'Owner',
      'Role',
      'Created',
      'Expires',
      'Last used',
      '',
    ])
    expect(cell(w, 0, 'Owner').text()).toBe('marlow')
    const router = cell(w, 1, 'Owner').get('span')
    expect(router.text()).toBe('router')
    expect(router.classes()).toContain('text-ink-muted')
  })

  it('makes no tokens', async () => {
    const w = await mounted()
    expect(w.text()).not.toContain('Add token')
  })

  it('deletes any token after the typed confirm', async () => {
    vi.mocked(api.tokens.remove).mockResolvedValue(undefined)
    const w = await mounted()
    const del = w.findAllComponents(stubs.ConfirmButton)[1]
    expect(del.props('typed')).toBe('nightly')
    del.vm.$emit('confirm')
    await flushPromises()
    expect(api.tokens.remove).toHaveBeenCalledWith('tk2')
    expect(api.tokens.list).toHaveBeenCalledTimes(2)
  })
})
