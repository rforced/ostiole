import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import CronDialog from '@/views/system/crons/CronDialog.vue'

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div v-if="open"><slot /></div>',
  },
}

function mountDialog(devices, role = 'operator') {
  useAuthStore().user = { username: role, role }
  const config = useConfigStore()
  config.replaceDraft({
    version: 6,
    zones: [{ name: 'lan' }],
    interfaces: [{ name: 'eth1', zone: 'lan', enabled: true }],
    services: { dhcp: { enabled: false }, dns: { enabled: false }, wol: { devices } },
  })
  const wrapper = mount(CronDialog, { props: { open: true }, global: { stubs } })
  return { wrapper, config }
}

describe('CronDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('schedules a wake for a device on the Wake on LAN page', async () => {
    const { wrapper, config } = mountDialog([
      { id: 'wol-nas', interface: 'eth1', mac: 'aa:bb:cc:00:00:01', description: 'NAS' },
      { id: 'wol-pc', interface: 'eth1', mac: 'aa:bb:cc:00:00:02' },
    ])
    await wrapper.get('#cron-kind').setValue('wake')
    const options = wrapper.findAll('#cron-device option')
    expect(options.map((o) => o.text())).toEqual(['NAS', 'aa:bb:cc:00:00:02'])
    await wrapper.get('#cron-device').setValue('wol-pc')
    await wrapper.get('form').trigger('submit')
    expect(config.crons).toHaveLength(1)
    expect(config.crons[0]).toMatchObject({ kind: 'wake', device: 'wol-pc' })
    expect(config.crons[0]).not.toHaveProperty('service')
  })

  it('says where devices come from when there are none', async () => {
    const { wrapper, config } = mountDialog(undefined)
    await wrapper.get('#cron-kind').setValue('wake')
    expect(wrapper.get('#cron-device').text()).toBe('None on the Wake on LAN page')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role="alert"]').text()).toBe('Add a device on the Wake on LAN page first.')
    expect(config.crons).toHaveLength(0)
  })

  // No shell runs the command, so a space or a comma is part of an argument.
  it('passes each line as one argument', async () => {
    const { wrapper, config } = mountDialog(undefined, 'admin')
    await wrapper.get('#cron-kind').setValue('command')
    await wrapper.get('#cron-command').setValue('/usr/local/bin/report')
    await wrapper.get('#cron-args').setValue('--subject=Nightly run\n\na,b')
    await wrapper.get('form').trigger('submit')
    expect(config.crons[0].args).toEqual(['--subject=Nightly run', 'a,b'])
  })
})
