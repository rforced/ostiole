import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import LogFilesFields from '@/views/system/LogFilesFields.vue'

vi.mock('@/lib/api', () => ({ api: { logFiles: vi.fn() } }))

const draft = (system = {}) => ({ version: 11, system, zones: [], interfaces: [], rules: [] })

async function open({ system = {}, status = { enabled: false, logs: [] } } = {}) {
  api.logFiles.mockResolvedValue({ dir: '/var/log/ostiole', ...status })
  const config = useConfigStore()
  config.saved = draft(system)
  config.replaceDraft(draft(system))
  const wrapper = mount(LogFilesFields)
  await flushPromises()
  return { wrapper, config }
}

describe('LogFilesFields', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // Off by default, and opening the page changes nothing.
  it('is off, and writes nothing into the draft by being opened', async () => {
    const { wrapper, config } = await open()
    expect(wrapper.get('input[type="checkbox"]').element.checked).toBe(false)
    expect(wrapper.find('#log-files-every').exists()).toBe(false)
    expect(wrapper.text()).toContain('The files hold client addresses.')
    expect(config.dirty).toBe(false)
  })

  it('writes the switch and the settings, and drops each back at its default', async () => {
    const { wrapper, config } = await open()
    await wrapper.get('input[type="checkbox"]').setValue(true)
    expect(config.draft.system.logging).toEqual({ files: { enabled: true } })
    const every = wrapper.get('#log-files-every')
    expect(every.element.value).toBe('5')
    await every.setValue('15')
    expect(config.draft.system.logging.files.writeMinutes).toBe(15)
    await every.setValue('5')
    expect(config.draft.system.logging.files.writeMinutes).toBeUndefined()

    const days = wrapper.get('#log-files-retention')
    expect(days.attributes('placeholder')).toBe('31')
    await days.setValue('90')
    expect(config.draft.system.logging.files.retentionDays).toBe(90)
    await days.setValue('')
    const gb = wrapper.get('#log-files-max-use')
    expect(gb.attributes('placeholder')).toBe('1')
    await gb.setValue('4')
    expect(config.draft.system.logging.files.maxUseGB).toBe(4)
    await gb.setValue('')

    // Off again with every value at its default: the draft is as saved.
    await wrapper.get('input[type="checkbox"]').setValue(false)
    expect(config.draft.system.logging).toBeUndefined()
    expect(config.dirty).toBe(false)
  })

  // The journal's own settings sit in the same block and stay.
  it('keeps the journal settings beside it', async () => {
    const { wrapper, config } = await open({ system: { logging: { level: 'info' } } })
    await wrapper.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('input[type="checkbox"]').setValue(false)
    expect(config.draft.system.logging).toEqual({ level: 'info' })
  })

  it('says what the files hold once they are on', async () => {
    let { wrapper } = await open({
      system: { logging: { files: { enabled: true } } },
      status: { enabled: true, logs: [{ name: 'firewall', bytes: 0, files: 0, unwritten: 3 }] },
    })
    expect(wrapper.text()).toContain('Not written yet.')

    const written = '2026-09-27T17:45:00Z'
    ;({ wrapper } = await open({
      system: { logging: { files: { enabled: true } } },
      status: {
        enabled: true,
        capped: true,
        logs: [
          { name: 'firewall', bytes: 3_000_000, files: 2, written, lost: 12 },
          { name: 'queries', bytes: 1_200_000, files: 1, written: '2026-09-27T17:40:00Z' },
        ],
      },
    }))
    const at = new Date(written).toLocaleString()
    expect(wrapper.text()).toContain(`4.2 MB in /var/log/ostiole, last written ${at}.`)
    expect(wrapper.text()).toContain(
      'Firewall log: 12 entries left memory before they were written.',
    )
    expect(wrapper.text()).toContain('The cap, not the days, decides how far back the files go.')
  })

  it('says why the files are not written', async () => {
    const { wrapper } = await open({
      system: { logging: { files: { enabled: true } } },
      status: {
        enabled: true,
        paused: true,
        logs: [
          {
            name: 'queries',
            error:
              'mkdir /var/log/ostiole/queries: read-only file system (this daemon’s unit does not open /var/log/ostiole yet: run `ostiole repair`)',
          },
        ],
      },
    })
    expect(wrapper.text()).toContain('The disk has less than 5% free, so the logs wait in memory.')
    expect(wrapper.text()).toContain('Query log: mkdir /var/log/ostiole/queries')
    expect(wrapper.text()).toContain('run `ostiole repair`')
  })
})
