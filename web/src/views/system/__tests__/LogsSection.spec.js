import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import { useToastStore } from '@/stores/toast'
import LogsSection from '@/views/system/LogsSection.vue'

vi.mock('@/lib/api', () => ({
  api: {
    logFiles: vi.fn(async () => ({ dir: '/var/log/ostiole', enabled: false, logs: [] })),
    clearLogs: vi.fn(),
  },
}))

const draft = (system = {}) => ({
  version: 11,
  system,
  zones: [],
  interfaces: [],
  rules: [],
})

function open(system = {}) {
  const config = useConfigStore()
  config.replaceDraft(draft(system))
  return { wrapper: mount(LogsSection), config }
}

describe('LogsSection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('shows warning when the configuration says nothing', () => {
    const { wrapper } = open()
    expect(wrapper.get('#logs-level-warning').element.checked).toBe(true)
  })

  // The saved configuration leaves defaults out. Opening the page used to
  // write an empty block, which was a change nobody made and stopped
  // signing out.
  it('changes nothing by being opened, and nothing once back at the defaults', async () => {
    const config = useConfigStore()
    config.saved = draft()
    const { wrapper } = open()
    expect(config.dirty).toBe(false)
    await wrapper.get('#logs-level-info').setValue()
    expect(config.dirty).toBe(true)
    await wrapper.get('#logs-level-warning').setValue()
    expect(config.dirty).toBe(false)
  })

  it('writes a chosen level into the draft', async () => {
    const { wrapper, config } = open()
    await wrapper.get('#logs-level-info').setValue()
    expect(config.draft.system.logging.level).toBe('info')
  })

  // The default is not a value: choosing it back takes the field out of
  // the draft rather than writing what the daemon would do anyway.
  it('clears the level when warning is chosen again', async () => {
    const { wrapper, config } = open({ logging: { level: 'debug' } })
    await wrapper.get('#logs-level-warning').setValue()
    expect(config.draft.system.logging?.level).toBeUndefined()
  })

  it('writes the retention and the ceiling and clears them when emptied', async () => {
    const { wrapper, config } = open()
    const days = wrapper.get('#logs-retention')
    expect(days.attributes('placeholder')).toBe('90')
    await days.setValue('30')
    expect(config.draft.system.logging.retentionDays).toBe(30)
    await days.setValue('')
    expect(config.draft.system.logging?.retentionDays).toBeUndefined()

    const gb = wrapper.get('#logs-max-use')
    expect(gb.attributes('placeholder')).toBe('10')
    await gb.setValue('25')
    expect(config.draft.system.logging.maxUseGB).toBe(25)
    await gb.setValue('')
    expect(config.draft.system.logging?.maxUseGB).toBeUndefined()
  })

  // One card for the whole block: the journal's settings, then the files'.
  it('holds the journal and the files, each under its name', async () => {
    const { wrapper, config } = open({ logging: { level: 'info' } })
    await flushPromises()
    const groups = wrapper.findAll('legend.group-title').map((l) => l.text())
    expect(groups).toEqual(['Journal', 'Files'])
    await wrapper.get('input[type="checkbox"]').setValue(true)
    expect(config.draft.system.logging).toEqual({ level: 'info', files: { enabled: true } })
  })

  it('is an operator’s to change and locked for a viewer', async () => {
    useAuthStore().user = { username: 'operator', role: 'operator' }
    let { wrapper } = open()
    expect(wrapper.find('fieldset[disabled]').exists()).toBe(false)
    useAuthStore().user = { username: 'viewer', role: 'viewer' }
    ;({ wrapper } = open())
    expect(wrapper.find('fieldset[disabled]').exists()).toBe(true)
    expect(wrapper.get('fieldset[disabled]').find('#logs-level-info').exists()).toBe(true)
    expect(wrapper.get('fieldset[disabled]').find('input[type="checkbox"]').exists()).toBe(true)
  })

  // Clear every log takes what the log pages clear, listed first, and the
  // files' status is read again after. Only an admin clears.
  it('clears every log, once asked', async () => {
    useAuthStore().user = { username: 'root', role: 'admin' }
    const ask = vi.spyOn(useConfirmStore(), 'ask').mockResolvedValue(true)
    const { wrapper, config } = open()
    await flushPromises()
    const clear = () => wrapper.findAll('button').find((b) => b.text() === 'Clear every log')
    const reads = api.logFiles.mock.calls.length
    await clear().trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenLastCalledWith(
      expect.objectContaining({
        question: 'Clear every log?',
        description: 'Each is emptied. The journal and traffic per link are kept.',
        dependentsLabel: 'Cleared',
        dependents: [
          'Firewall log',
          'Query log',
          'WAF events',
          'Proxy requests',
          'DHCP log',
          'Wireless log',
          'WireGuard log',
          'Tailscale log',
          'Drive history',
          'Traffic per device',
          'Destinations',
        ],
      }),
    )
    expect(api.clearLogs).toHaveBeenCalledOnce()
    expect(useToastStore().toasts.map((t) => t.message)).toEqual(['Every log is cleared.'])
    expect(api.logFiles.mock.calls.length).toBeGreaterThan(reads)

    config.saved = draft({ logging: { files: { enabled: true } } })
    await flushPromises()
    await clear().trigger('click')
    expect(ask).toHaveBeenLastCalledWith(
      expect.objectContaining({
        description:
          'Each is emptied, and its files are deleted. The journal and traffic per link are kept.',
      }),
    )
  })

  it('greys Clear every log for an operator, and leaves it out for a viewer', () => {
    useAuthStore().user = { username: 'operator', role: 'operator' }
    let { wrapper } = open()
    const clear = wrapper.findAll('button').find((b) => b.text() === 'Clear every log')
    expect(clear.attributes('disabled')).toBeDefined()
    expect(clear.attributes('title')).toBe('Only an admin can clear them.')
    useAuthStore().user = { username: 'viewer', role: 'viewer' }
    ;({ wrapper } = open())
    expect(wrapper.findAll('button').some((b) => b.text() === 'Clear every log')).toBe(false)
  })
})
