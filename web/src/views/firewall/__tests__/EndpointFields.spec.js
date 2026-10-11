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

describe('EndpointFields zone', () => {
  beforeEach(() => setActivePinia(createPinia()))

  const kinds = (w) =>
    w.findAll(`#${w.props('side')}-mode option`).map((o) => o.attributes('value'))

  // A zone is somewhere traffic goes, so only a destination can name one.
  it('offers a zone to a destination, right after Any, and to a source never', () => {
    expect(kinds(mountFields('destination', 'lan', []))).toEqual([
      'any',
      'zone',
      'addresses',
      'alias',
      'peer',
      'self',
    ])
    expect(kinds(mountFields('source', 'lan', []))).toEqual(['any', 'addresses', 'alias', 'peer'])
  })

  it('picks the zone by name, and has nothing to invert', async () => {
    const w = mountFields('destination', 'lan', [])
    await w.get('#destination-mode').setValue('addresses')
    expect(w.find('#destination-not').exists()).toBe(true)
    expect(w.find('#destination-zone').exists()).toBe(false)

    await w.get('#destination-mode').setValue('zone')
    const select = w.get('#destination-zone')
    expect(select.attributes('required')).toBeDefined()
    expect(select.findAll('option').map((o) => o.text())).toEqual(['lan', 'devices', 'sites'])
    expect(w.get('label[for="destination-zone"]').text()).toBe('Zone')
    expect(w.text()).toContain('Forwarded traffic headed into the zone, whichever networks it has.')
    expect(w.find('#destination-not').exists()).toBe(false)

    await select.setValue('sites')
    expect(w.props('modelValue').zone).toBe('sites')
  })

  it('keeps the ports a zone can be asked for', () => {
    useConfigStore().replaceDraft(draft([]))
    const model = { mode: 'zone', zone: 'devices', notAddresses: false, portMode: 'any' }
    const w = mount(EndpointFields, {
      props: { side: 'destination', portsAllowed: true, modelValue: model },
    })
    expect(w.get('#destination-zone').element.value).toBe('devices')
    expect(w.find('#destination-portmode').exists()).toBe(true)
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
          AppDialog: {
            props: ['open', 'title', 'description'],
            template: '<div><slot /><slot name="footer" /></div>',
          },
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
