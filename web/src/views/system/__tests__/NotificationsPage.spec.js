import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import NotificationsPage from '@/views/system/NotificationsPage.vue'

vi.mock('@/lib/api', () => ({
  api: { notifications: { status: vi.fn(), test: vi.fn() } },
}))

const config = (notifications) => ({
  version: 7,
  system: {},
  zones: [],
  interfaces: [],
  rules: [],
  ...(notifications ? { notifications } : {}),
})

async function open(notifications, role = 'admin') {
  useAuthStore().user = { username: role, role }
  const store = useConfigStore()
  store.saved = config(notifications)
  store.replaceDraft(config(notifications))
  const wrapper = mount(NotificationsPage)
  await flushPromises()
  return { wrapper, store }
}

const button = (w, text) => w.findAll('button').find((b) => b.text().includes(text))
const toggle = (w, label) =>
  w.findAll('input[type="checkbox"]').find((i) => i.element.labels[0]?.textContent.includes(label))

describe('NotificationsPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.notifications.status.mockResolvedValue({ targets: {}, recent: [] })
  })

  it('writes mail settings into the draft and takes them out again', async () => {
    const { wrapper, store } = await open()
    await toggle(wrapper, 'Send mail').setValue(true)
    await wrapper.get('#nt-server').setValue('smtp.example.net')
    await wrapper.get('#nt-security').setValue('tls')
    await wrapper.get('#nt-to').setValue('a@example.net, b@example.net')
    expect(store.draft.notifications.email).toEqual({
      enabled: true,
      server: 'smtp.example.net',
      security: 'tls',
      to: ['a@example.net', 'b@example.net'],
    })
    // Put back, the draft matches what is saved and the apply bar stays quiet.
    await toggle(wrapper, 'Send mail').setValue(false)
    await wrapper.get('#nt-server').setValue('')
    await wrapper.get('#nt-security').setValue('starttls')
    await wrapper.get('#nt-to').setValue('')
    expect(store.draft.notifications).toBeUndefined()
    expect(store.dirty).toBe(false)
  })

  it('mutes a kind by switching it off', async () => {
    const { wrapper, store } = await open({ enabled: true })
    const waf = toggle(wrapper, 'web application firewall')
    expect(waf.element.checked).toBe(true)
    await waf.setValue(false)
    expect(store.draft.notifications.mute).toEqual(['waf'])
    await waf.setValue(true)
    expect(store.draft.notifications.mute).toBeUndefined()
  })

  // The test goes to what the form says, before anything is applied.
  it('sends a test from the draft', async () => {
    api.notifications.test.mockResolvedValue([
      { target: 'email' },
      { target: 'webhook', error: 'hooks.example.net answered 404 Not Found' },
    ])
    const settings = { email: { enabled: true, server: 'smtp.example.net' } }
    const { wrapper } = await open(settings)
    await button(wrapper, 'Send a test').trigger('click')
    await flushPromises()
    expect(api.notifications.test).toHaveBeenCalledWith(settings)
    expect(wrapper.text()).toContain('Mail: sent.')
    expect(wrapper.text()).toContain('Webhook: hooks.example.net answered 404 Not Found')
  })

  it('lists what went out and where', async () => {
    api.notifications.status.mockResolvedValue({
      targets: { email: { lastTried: '2026-09-24T13:04:00Z', lastSent: '2026-09-24T13:04:00Z' } },
      recent: [
        {
          time: '2026-09-24T13:04:00Z',
          events: [{ kind: 'gateway-down', level: 'ok', title: 'Gateway wan is down' }],
          sent: ['email'],
          failed: { webhook: 'refused' },
        },
      ],
    })
    const { wrapper } = await open({ enabled: true })
    const text = wrapper.text()
    expect(text).toContain('Resolved: Gateway wan is down')
    expect(text).toContain('Webhook: refused')
    expect(text).not.toContain('Nothing sent.')
  })

  // Where the router's news goes is an admin's to say; everyone else reads.
  it('keeps the settings from an operator', async () => {
    const { wrapper } = await open({ enabled: true }, 'operator')
    expect(wrapper.text()).toContain('Only an admin can change these.')
    expect(wrapper.get('#nt-enabled').attributes('disabled')).toBeDefined()
    for (const f of wrapper.findAll('fieldset')) expect(f.attributes('disabled')).toBeDefined()
    expect(button(wrapper, 'Send a test').attributes('disabled')).toBeDefined()
  })
})
