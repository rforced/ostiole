import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import ProtectionPage from '@/views/firewall/ProtectionPage.vue'

const stubs = { RouterLink: true }

/** A draft with an outside, an inside, and one rule that is rationed. */
function draft() {
  return {
    version: 6,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [{ name: 'eth0', zone: 'wan' }],
    rules: [
      {
        id: 'ssh-in',
        zone: 'wan',
        description: 'SSH',
        limit: { rate: 3, unit: 'minute', burst: 3, perSource: true },
      },
      { id: 'web-in', zone: 'wan', description: 'Web' },
    ],
    protection: {},
    system: {},
  }
}

function page() {
  const config = useConfigStore()
  config.draft = draft()
  return { wrapper: mount(ProtectionPage, { global: { stubs } }), config }
}

describe('ProtectionPage', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('defends the zones that face the internet without being told', () => {
    const { wrapper } = page()
    const wan = wrapper.find('#prot-zone-wan')
    const lan = wrapper.find('#prot-zone-lan')
    expect(wan.element.checked).toBe(true)
    expect(lan.element.checked).toBe(false)
  })

  it('records a choice of zones, and forgets it when it matches the default', async () => {
    const { wrapper, config } = page()
    await wrapper.find('#prot-zone-lan').setValue(true)
    expect(config.draft.protection.zones).toEqual(['wan', 'lan'])
    // Back to just the external zone, which is the default, so the list
    // goes away rather than being written out.
    await wrapper.find('#prot-zone-lan').setValue(false)
    expect(config.draft.protection.zones).toBeUndefined()
  })

  it('switches a defence on with settings that do not fire on a quiet network', async () => {
    const { wrapper, config } = page()
    await wrapper.find('#prot-syn').setValue(true)
    expect(config.draft.protection.synFlood).toEqual({
      rate: 30,
      unit: 'second',
      burst: 60,
      perSource: true,
    })
    // The fields appear only once it is on.
    expect(wrapper.find('#prot-syn-rate').exists()).toBe(true)
    await wrapper.find('#prot-syn-rate').setValue('50')
    expect(config.draft.protection.synFlood.rate).toBe(50)
  })

  it('removes a defence rather than leaving it switched off', async () => {
    const { wrapper, config } = page()
    await wrapper.find('#prot-scan').setValue(true)
    expect(config.draft.protection.portScan.hold).toBe('10m')
    await wrapper.find('#prot-scan').setValue(false)
    expect(config.draft.protection.portScan).toBeUndefined()
  })

  it('lists the rules that hold their traffic to a rate', () => {
    const { wrapper } = page()
    const text = wrapper.text()
    expect(text).toContain('3/minute burst 3')
    expect(text).toContain('per source')
    // A rule with no limit is not in the table.
    expect(text).not.toContain('Web')
  })

  it('leaves the connection ceiling to the kernel until a number is typed', async () => {
    const { wrapper, config } = page()
    const field = wrapper.find('#prot-conntrack')
    expect(field.element.value).toBe('')

    await field.setValue(1000000)
    expect(config.draft.system.conntrackMax).toBe(1000000)

    // Cleared is the kernel's own limit again, not a ceiling of zero.
    await field.setValue('')
    expect(config.draft.system.conntrackMax).toBeUndefined()
  })
})
