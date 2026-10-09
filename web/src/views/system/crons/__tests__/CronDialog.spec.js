import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import CronDialog from '@/views/system/crons/CronDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

function open(cron) {
  const config = useConfigStore()
  const cfg = { version: 12, zones: [], interfaces: [], rules: [], crons: [cron] }
  config.saved = cfg
  config.replaceDraft(cfg)
  const wrapper = mount(CronDialog, {
    props: { open: true, cron: config.crons[0] },
    global: { stubs },
  })
  return { wrapper, config }
}

describe('CronDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reopens a backup as saved and leaves an untouched save alone', async () => {
    const { wrapper, config } = open({
      id: 'c1',
      enabled: true,
      schedule: '0 4 * * *',
      kind: 'backup',
    })
    expect(wrapper.get('#cron-keep').element.value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('reopens a command as saved', async () => {
    const { wrapper, config } = open({
      id: 'c2',
      enabled: true,
      schedule: '@daily',
      kind: 'command',
      command: '/usr/local/bin/made-up-report',
    })
    expect(wrapper.get('#cron-timeout').element.value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})
