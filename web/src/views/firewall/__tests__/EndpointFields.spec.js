import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import EndpointFields from '@/views/firewall/EndpointFields.vue'
import RuleDialog from '@/views/firewall/RuleDialog.vue'

const tunnel = (name, zone, peers) => ({ name, zone, enabled: true, wireguard: { peers } })

function draft(interfaces) {
  return {
    version: 11,
    zones: [{ name: 'lan' }, { name: 'devices' }, { name: 'sites' }],
    interfaces: [{ name: 'eth1', zone: 'lan', enabled: true }, ...interfaces],
    rules: [],
    nat: { outbound: { mode: 'automatic' } },
  }
}

const devices = tunnel('wg0', 'devices', [
  { name: 'laptop', allowedIps: ['10.66.0.2/32'] },
  { name: 'vpn', allowedIps: ['0.0.0.0/0'] },
])
const sites = tunnel('wg1', 'sites', [{ name: 'friend', allowedIps: ['192.168.50.0/24'] }])

function mountFields(side, zone, interfaces) {
  useConfigStore().replaceDraft(draft(interfaces))
  const model = { mode: 'peer', peer: '', notAddresses: false, portMode: 'any' }
  return mount(EndpointFields, { props: { side, zone, modelValue: model } })
}

const peerOption = (w) => w.get(`#${w.props('side')}-mode`).find('option[value="peer"]')
const peerChoices = (w) => w.findAll(`#${w.props('side')}-peer option`).map((o) => o.text())

describe('EndpointFields peers', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('offers no peer where there is none', () => {
    const w = mountFields('destination', 'lan', [])
    expect(peerOption(w).attributes('disabled')).toBeDefined()
  })

  // A source has to arrive in the rule's zone, and a peer that takes a
  // default route stands for the whole internet.
  it('offers the peers a rule can name', () => {
    expect(peerChoices(mountFields('source', 'devices', [devices, sites]))).toEqual([
      'laptop on wg0',
    ])
    expect(peerChoices(mountFields('destination', 'lan', [devices, sites]))).toEqual([
      'laptop on wg0',
      'friend on wg1',
    ])
  })
})

describe('RuleDialog with a peer', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps a peer through an edit', async () => {
    const config = useConfigStore()
    config.replaceDraft(draft([devices, sites]))
    const rule = {
      id: 'r1',
      zone: 'lan',
      enabled: true,
      action: 'accept',
      protocol: 'tcp',
      source: {},
      destination: { peer: 'wg1/friend', notAddresses: true, ports: ['22'] },
    }
    config.draft.rules.push(rule)
    const wrapper = mount(RuleDialog, {
      props: { open: true, rule, zone: 'lan' },
      global: {
        stubs: {
          AppDialog: { props: ['open', 'title', 'description'], template: '<div><slot /></div>' },
        },
      },
    })
    expect(wrapper.get('#destination-mode').element.value).toBe('peer')
    expect(wrapper.get('#destination-peer').element.value).toBe('wg1/friend')
    await wrapper.get('form').trigger('submit')
    expect(config.rules[0].destination).toEqual({
      peer: 'wg1/friend',
      notAddresses: true,
      ports: ['22'],
    })
  })
})
