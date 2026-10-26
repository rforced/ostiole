import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import type { Config, Schedule } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'
import { draftOf, emptyConfig, must } from '@/test/fixtures'
import ScheduleDialog from '@/views/firewall/ScheduleDialog.vue'

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

const DAYS = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday']

function open(schedule: Schedule) {
  const cfg: Config = { ...emptyConfig(), schedules: [schedule] }
  const config = useConfigStore()
  config.saved = JSON.parse(JSON.stringify(cfg))
  config.replaceDraft(cfg)
  const wrapper = mount(ScheduleDialog, {
    props: { open: true, schedule: must(draftOf(config).schedules)[0] },
    global: { stubs },
  })
  return { wrapper, config }
}

describe('ScheduleDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps a stored list of every day on an untouched save', async () => {
    const { wrapper, config } = open({
      name: 'opening',
      days: [...DAYS],
      start: '08:00',
      end: '17:00',
    })
    const boxes = wrapper.findAll<HTMLInputElement>('input[type=checkbox]')
    expect(boxes).toHaveLength(7)
    expect(boxes.every((b) => b.element.checked)).toBe(true)
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })

  it('writes the days the operator leaves ticked', async () => {
    const { wrapper, config } = open({
      name: 'opening',
      days: [...DAYS],
      start: '08:00',
      end: '17:00',
    })
    await wrapper.findAll<HTMLInputElement>('input[type=checkbox]')[6].setValue(false)
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(true)
    expect(must(draftOf(config).schedules)[0].days).toEqual(DAYS.slice(0, 6))
  })
})
