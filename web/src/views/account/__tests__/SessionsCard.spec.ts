import { type DOMWrapper, type VueWrapper, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { type SessionRow, api } from '@/lib/api'
import { must } from '@/test/fixtures'
import { useConfirmStore } from '@/stores/confirm'
import SessionsCard from '@/views/account/SessionsCard.vue'

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  api: { auth: { sessions: vi.fn(), endSession: vi.fn(), endOtherSessions: vi.fn() } },
}))

const FIREFOX = 'Mozilla/5.0 (X11; Linux x86_64; rv:141.0) Gecko/20100101 Firefox/141.0'
const PHONE =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1'

const session = (id: string, agent: string, address: string, current = false): SessionRow => ({
  id,
  created: '2026-10-01T08:00:00Z',
  lastSeen: '2026-10-11T09:30:00Z',
  expires: '2026-10-25T09:30:00Z',
  address,
  agent,
  current,
})

const here = session('a1b2c3d4e5f60718', FIREFOX, '192.0.2.10', true)
const phone = session('0f1e2d3c4b5a6978', PHONE, '198.51.100.7')
const script = session('9a8b7c6d5e4f3021', '', '')

async function open(rows: SessionRow[] = [here, phone, script]) {
  vi.mocked(api.auth.sessions).mockResolvedValue(rows)
  const wrapper = mount(SessionsCard)
  await flushPromises()
  return wrapper
}

const findRow = (wrapper: VueWrapper, text: string) =>
  wrapper.findAll('tbody tr').find((tr) => tr.get('td').text().startsWith(text))
const row = (wrapper: VueWrapper, text: string) => {
  const found = findRow(wrapper, text)
  if (!found) throw new Error(`no row ${text}`)
  return found
}

const findButton = (scope: Pick<DOMWrapper<Element>, 'findAll'>, text: string) =>
  scope.findAll('button').find((b) => b.text() === text)
const button = (scope: Pick<DOMWrapper<Element>, 'findAll'>, text: string) => {
  const found = findButton(scope, text)
  if (!found) throw new Error(`no button ${text}`)
  return found
}

describe('SessionsCard', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('lists every session with its browser and address', async () => {
    const wrapper = await open()
    expect(wrapper.text()).toContain('Sessions')
    expect(row(wrapper, 'Firefox on Linux').text()).toContain('192.0.2.10')
    expect(row(wrapper, 'Safari on iPhone').text()).toContain('198.51.100.7')
    expect(must(row(wrapper, 'unknown').findAll('td')[1]).text()).toBe('unknown')
  })

  // Signing this browser out is the user menu's job.
  it('marks this browser and offers no sign out on it', async () => {
    const wrapper = await open()
    const mine = row(wrapper, 'Firefox on Linux')
    expect(mine.get('td').text()).toContain('this browser')
    expect(findButton(mine, 'Sign out')).toBeUndefined()
    expect(button(row(wrapper, 'Safari on iPhone'), 'Sign out')).toBeDefined()
    expect(row(wrapper, 'Safari on iPhone').get('td').text()).not.toContain('this browser')
  })

  it('signs one session out and reads the list again', async () => {
    const wrapper = await open()
    vi.mocked(api.auth.endSession).mockResolvedValue(undefined)
    vi.mocked(api.auth.sessions).mockResolvedValue([here, script])

    await button(row(wrapper, 'Safari on iPhone'), 'Sign out').trigger('click')
    await flushPromises()

    expect(api.auth.endSession).toHaveBeenCalledWith(phone.id)
    expect(api.auth.sessions).toHaveBeenCalledTimes(2)
    expect(findRow(wrapper, 'Safari on iPhone')).toBeUndefined()
  })

  it('shows what the server said when a sign out is refused', async () => {
    const wrapper = await open()
    vi.mocked(api.auth.endSession).mockRejectedValue(new Error('no such session'))

    await button(row(wrapper, 'Safari on iPhone'), 'Sign out').trigger('click')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('no such session')
    expect(api.auth.sessions).toHaveBeenCalledTimes(2)
  })

  it('signs the others out once the confirm says yes', async () => {
    const wrapper = await open()
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    vi.mocked(api.auth.endOtherSessions).mockResolvedValue(undefined)
    vi.mocked(api.auth.sessions).mockResolvedValue([here])

    await button(wrapper, 'Sign out the others').trigger('click')
    await flushPromises()

    expect(ask).toHaveBeenCalledWith({
      question: 'Sign out every other session?',
      description: 'They sign in again next time.',
      confirmLabel: 'Sign out',
    })
    expect(api.auth.endOtherSessions).toHaveBeenCalled()
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(button(wrapper, 'Sign out the others').attributes('disabled')).toBeDefined()
  })

  it('leaves the others when the confirm is declined', async () => {
    const wrapper = await open()
    vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(false)
    await button(wrapper, 'Sign out the others').trigger('click')
    await flushPromises()
    expect(api.auth.endOtherSessions).not.toHaveBeenCalled()
  })

  it('has no others to sign out when this browser is the only one', async () => {
    const wrapper = await open([here])
    expect(button(wrapper, 'Sign out the others').attributes('disabled')).toBeDefined()
  })
})
