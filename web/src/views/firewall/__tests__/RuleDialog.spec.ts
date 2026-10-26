import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import type { Config, Rule } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'
import { emptyConfig, iface } from '@/test/fixtures'
import RuleDialog from '@/views/firewall/RuleDialog.vue'

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
  EndpointFields: true,
}

function draft(shaped: boolean): Config {
  return {
    ...emptyConfig(),
    zones: [{ name: 'lan' }, { name: 'wan', external: true }],
    interfaces: [
      iface({
        name: 'eth0',
        zone: 'wan',
        enabled: true,
        ...(shaped ? { shaping: { download: 1e8 } } : {}),
      }),
      iface({ name: 'eth1', zone: 'lan', enabled: true }),
    ],
  }
}

function open(shaped: boolean, rule: Rule | null = null) {
  const config = useConfigStore()
  config.replaceDraft(draft(shaped))
  const wrapper = mount(RuleDialog, {
    props: { open: true, rule, zone: 'lan' },
    global: { stubs },
  })
  return { wrapper, config }
}

describe('RuleDialog priority', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // Asking for a priority on a router that shapes nothing offers a choice
  // with no consequence, so the field is not there at all.
  it('is hidden until something has a line speed', () => {
    expect(open(false).wrapper.find('#rule-priority').exists()).toBe(false)
    expect(open(true).wrapper.find('#rule-priority').exists()).toBe(true)
  })

  it('stores the tier on an accept rule', async () => {
    const { wrapper, config } = open(true)
    await wrapper.get('#rule-priority').setValue('realtime')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0].priority).toBe('realtime')
  })

  // A dropped connection has no traffic to prioritise, so the field goes
  // off and takes whatever was chosen with it rather than saving something
  // the router would refuse.
  it('is turned off, and cleared, by a rule that does not accept', async () => {
    const { wrapper, config } = open(true)
    await wrapper.get('#rule-priority').setValue('high')
    await wrapper.get('#rule-action').setValue('drop')
    const select = wrapper.get<HTMLSelectElement>('#rule-priority')
    expect(select.attributes('disabled')).toBeDefined()
    expect(select.element.value).toBe('')
    expect(wrapper.text()).toContain('Only an accept rule can set a priority.')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0].priority).toBeUndefined()
  })

  // Setting nothing and setting Normal are next to each other in the same
  // list and do different things, so they must not read the same.
  it('tells "not set" apart from Normal', () => {
    const { wrapper } = open(true)
    const labels = wrapper
      .get('#rule-priority')
      .findAll('option')
      .map((o) => o.text())
    expect(labels[0]).toContain('the device')
    expect(labels[0]).not.toContain('Normal')
    expect(labels.filter((l) => l.startsWith('Normal'))).toHaveLength(1)
    expect(wrapper.text()).toContain('Unset leaves it to what the device asks for')
  })

  it('reads an existing tier back', () => {
    const rule: Rule = {
      id: 'r1',
      zone: 'lan',
      enabled: true,
      action: 'accept',
      protocol: 'any',
      source: {},
      destination: {},
      priority: 'bulk',
    }
    expect(open(true, rule).wrapper.get<HTMLSelectElement>('#rule-priority').element.value).toBe(
      'bulk',
    )
  })
})

describe('RuleDialog limit', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reopens a limited rule as saved and leaves an untouched save alone', async () => {
    const config = useConfigStore()
    const cfg: Config = {
      ...draft(false),
      rules: [
        {
          id: 'r1',
          enabled: true,
          zone: 'lan',
          action: 'accept',
          protocol: 'any',
          source: {},
          destination: {},
          limit: { rate: 20, unit: 'minute' },
        },
      ],
    }
    config.saved = cfg
    config.replaceDraft(cfg)
    const wrapper = mount(RuleDialog, {
      props: { open: true, rule: config.rules[0], zone: 'lan' },
      global: { stubs },
    })
    await wrapper.get('form').trigger('submit')
    expect(config.dirty).toBe(false)
  })
})

// The zone is picked among the destination's own fields, so these mount them.
const realFields = { AppDialog: stubs.AppDialog }

function openZone(rule: Rule | null = null) {
  const config = useConfigStore()
  const cfg = {
    ...draft(false),
    gateways: [{ name: 'wan1', enabled: true, interface: 'eth0' }],
    rules: rule ? [rule] : [],
  } as Config
  config.saved = cfg
  config.replaceDraft(cfg)
  const wrapper = mount(RuleDialog, {
    props: { open: true, rule: rule ? config.rules[0] : null, zone: 'lan' },
    global: { stubs: realFields },
  })
  return { wrapper, config }
}

const saved = (extra?: Partial<Rule>): Rule => ({
  id: 'r1',
  enabled: true,
  zone: 'lan',
  action: 'accept',
  protocol: 'any',
  source: {},
  destination: {},
  ...extra,
})

describe('RuleDialog destination zone', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('saves the zone as destZone and nothing of an address with it', async () => {
    const { wrapper, config } = openZone()
    await wrapper.get('#destination-mode').setValue('addresses')
    await wrapper.get('#destination-addresses').setValue('203.0.113.0/24')
    await wrapper.get('#destination-not').setValue(true)
    await wrapper.get('#destination-mode').setValue('zone')
    await wrapper.get('#destination-zone').setValue('wan')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0].destZone).toBe('wan')
    expect(config.rules[0].destination).toEqual({})
  })

  it('keeps ports beside the zone', async () => {
    const { wrapper, config } = openZone()
    await wrapper.get('#rule-proto').setValue('tcp')
    await wrapper.get('#destination-mode').setValue('zone')
    await wrapper.get('#destination-zone').setValue('wan')
    await wrapper.get('#destination-portmode').setValue('ports')
    await wrapper.get('#destination-ports').setValue('25')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0].destZone).toBe('wan')
    expect(config.rules[0].destination).toEqual({ ports: ['25'] })
  })

  it('reads a saved zone back and leaves an untouched save alone', async () => {
    const rules = [
      saved({ destZone: 'lan' }),
      saved({ destZone: 'wan', protocol: 'tcp', destination: { ports: ['25'] } }),
    ]
    for (const rule of rules) {
      const { wrapper, config } = openZone(rule)
      expect(wrapper.get<HTMLSelectElement>('#destination-mode').element.value).toBe('zone')
      expect(wrapper.get<HTMLSelectElement>('#destination-zone').element.value).toBe(rule.destZone)
      await wrapper.get('form').trigger('submit')
      expect(config.rules[0]).toEqual(rule)
      expect(config.dirty).toBe(false)
    }
  })

  it('writes no destZone for a rule that had none', async () => {
    const { wrapper, config } = openZone(saved())
    expect(wrapper.get<HTMLSelectElement>('#destination-mode').element.value).toBe('any')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0]).not.toHaveProperty('destZone')
    expect(config.dirty).toBe(false)
  })

  it('drops the zone when the destination goes back to Any', async () => {
    const { wrapper, config } = openZone(saved({ destZone: 'wan' }))
    await wrapper.get('#destination-mode').setValue('any')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0]).not.toHaveProperty('destZone')
  })

  it('names the zone the traffic enters', () => {
    const { wrapper } = openZone()
    expect(wrapper.findComponent(stubs.AppDialog).props('description')).toBe(
      'Traffic entering lan, whether for this router or forwarded through it.',
    )
  })

  // A gateway decides where the traffic leaves, so it and a zone to leave
  // by exclude one another; going back to Any gives the choice back.
  it('turns the gateway off, and clears it, while the destination is a zone', async () => {
    const { wrapper, config } = openZone()
    const gateway = wrapper.get<HTMLSelectElement>('#rule-gateway')
    await gateway.setValue('wan1')
    expect(gateway.attributes('disabled')).toBeUndefined()

    await wrapper.get('#destination-mode').setValue('zone')
    await wrapper.get('#destination-zone').setValue('wan')
    expect(gateway.attributes('disabled')).toBeDefined()
    expect(gateway.element.value).toBe('')
    expect(wrapper.text()).toContain(
      'Only an accept rule whose destination is not a zone can choose a gateway.',
    )

    await wrapper.get('#destination-mode').setValue('any')
    expect(gateway.attributes('disabled')).toBeUndefined()
    await gateway.setValue('wan1')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0].gateway).toBe('wan1')
    expect(config.rules[0]).not.toHaveProperty('destZone')
  })
})
