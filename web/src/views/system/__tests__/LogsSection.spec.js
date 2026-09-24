import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import LogsSection from '@/views/system/LogsSection.vue'

const draft = (system = {}) => ({
  version: 6,
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
})
