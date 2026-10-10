import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import EnforcementTab from '@/views/services/dns/EnforcementTab.vue'

const stubs = { RouterLink: true }

/** Three zones behind the router and one outside it, saved the way Go writes them. */
function draft() {
  return {
    version: 11,
    zones: [{ name: 'lan' }, { name: 'wan', external: true }, { name: 'guest' }, { name: 'lab' }],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true },
      { name: 'eth1', zone: 'lan', enabled: true },
      { name: 'eth2', zone: 'guest', enabled: true },
      { name: 'eth3', zone: 'lab', enabled: true },
    ],
    rules: [],
    services: { dns: { enabled: true } },
    blocking: { enforce: { redirectDns: true, blockDot: true } },
  }
}

async function tab(cfg = draft()) {
  const config = useConfigStore()
  config.saved = JSON.parse(JSON.stringify(cfg))
  config.replaceDraft(cfg)
  config.loaded = true
  const wrapper = mount(EnforcementTab, { global: { stubs } })
  await flushPromises()
  return { wrapper, config }
}

/** Each zone on offer, in order, and whether its box is ticked. */
function boxes(wrapper) {
  return wrapper
    .findAll('input[id^="enf-zone-"]')
    .map((b) => [b.attributes('id').slice('enf-zone-'.length), b.element.checked])
}

describe('EnforcementTab', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('offers the zones behind the router, all ticked while none is chosen', async () => {
    const { wrapper } = await tab()
    expect(wrapper.findAll('h2').map((h) => h.text())).toEqual([
      'Where',
      'Keep clients here',
      'Encrypted DNS',
    ])
    expect(boxes(wrapper)).toEqual([
      ['lan', true],
      ['guest', true],
      ['lab', true],
    ])
    expect(wrapper.findAll('label[for^="enf-zone-"]').map((l) => l.text())).toEqual([
      'lan',
      'guest',
      'lab',
    ])
    expect(wrapper.find('#enf-zone-wan').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Nothing is enforced')
  })

  it('opens without changing the draft', async () => {
    const { config } = await tab()
    expect(config.dirty).toBe(false)
  })

  it('writes the zones left when one is unticked, and drops the list when it is ticked again', async () => {
    const { wrapper, config } = await tab()
    await wrapper.find('#enf-zone-lan').setValue(false)
    expect(config.draft.blocking.enforce.zones).toEqual(['guest', 'lab'])
    expect(boxes(wrapper)).toEqual([
      ['lan', false],
      ['guest', true],
      ['lab', true],
    ])

    await wrapper.find('#enf-zone-lan').setValue(true)
    expect('zones' in config.draft.blocking.enforce).toBe(false)
    expect(config.dirty).toBe(false)
  })

  it('ticks only the zones saved', async () => {
    const cfg = draft()
    cfg.blocking.enforce.zones = ['lan']
    const { wrapper, config } = await tab(cfg)
    expect(boxes(wrapper)).toEqual([
      ['lan', true],
      ['guest', false],
      ['lab', false],
    ])
    expect(config.dirty).toBe(false)
  })

  it('writes the zones in configuration order', async () => {
    const cfg = draft()
    cfg.blocking.enforce.zones = ['lab']
    const { wrapper, config } = await tab(cfg)
    await wrapper.find('#enf-zone-lan').setValue(true)
    expect(config.draft.blocking.enforce.zones).toEqual(['lan', 'lab'])
  })

  // None chosen means every zone, so the last box unticked comes back
  // ticked with the rest rather than showing a zone that is still enforced
  // as off.
  it('goes back to every zone when the last one is unticked', async () => {
    const cfg = draft()
    cfg.blocking.enforce.zones = ['lan']
    const { wrapper, config } = await tab(cfg)
    await wrapper.find('#enf-zone-lan').setValue(false)
    expect('zones' in config.draft.blocking.enforce).toBe(false)
    expect(boxes(wrapper)).toEqual([
      ['lan', true],
      ['guest', true],
      ['lab', true],
    ])
  })

  it('keeps the only zone ticked and the draft as it was', async () => {
    const cfg = draft()
    cfg.zones = [{ name: 'lan' }, { name: 'wan', external: true }]
    const { wrapper, config } = await tab(cfg)
    await wrapper.find('#enf-zone-lan').setValue(false)
    expect(boxes(wrapper)).toEqual([['lan', true]])
    expect(config.dirty).toBe(false)
  })

  it('says nothing is enforced when every zone faces the internet', async () => {
    const cfg = draft()
    cfg.zones = [{ name: 'wan', external: true }]
    const { wrapper } = await tab(cfg)
    expect(boxes(wrapper)).toEqual([])
    expect(wrapper.text()).toContain('Nothing is enforced: every zone here faces the internet.')
  })
})
