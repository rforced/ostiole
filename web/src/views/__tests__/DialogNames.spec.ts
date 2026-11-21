import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Component } from 'vue'

import { useConfigStore } from '@/stores/config'
import { configOf, emptyConfig, iface, must } from '@/test/fixtures'
import AliasDialog from '@/views/firewall/AliasDialog.vue'
import ScheduleDialog from '@/views/firewall/ScheduleDialog.vue'
import ZoneDialog from '@/views/interfaces/ZoneDialog.vue'
import GatewayDialog from '@/views/routing/GatewayDialog.vue'
import GatewayGroupDialog from '@/views/routing/GatewayGroupDialog.vue'
import BlockListDialog from '@/views/services/dns/BlockListDialog.vue'
import PoolDialog from '@/views/services/proxy/PoolDialog.vue'
import ProfileDialog from '@/views/services/proxy/ProfileDialog.vue'
import SiteDialog from '@/views/services/proxy/SiteDialog.vue'
import AccountDialog from '@/views/system/certificates/AccountDialog.vue'
import ProviderDialog from '@/views/system/providers/ProviderDialog.vue'
import PeerDialog from '@/views/vpn/PeerDialog.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const mod = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...mod,
    api: {
      ...mod.api,
      blocking: { catalog: vi.fn().mockResolvedValue([]) },
      certificates: { key: vi.fn().mockResolvedValue({ privateKey: 'k' }) },
    },
  }
})

const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

function draft() {
  const base = emptyConfig()
  return configOf({
    ...base,
    zones: [{ name: 'lan' }, { name: 'wan', external: true }],
    interfaces: [
      iface({ name: 'eth0', zone: 'wan', enabled: true, ipv4: { mode: 'dhcp' } }),
      iface({
        name: 'wg0',
        zone: 'lan',
        enabled: true,
        ipv4: { mode: 'static', address: '192.0.2.1/24' },
        wireguard: {
          privateKey: 'x',
          peers: [{ name: 'laptop', enabled: true, publicKey: 'k', allowedIps: ['192.0.2.2/32'] }],
        },
      }),
    ],
    aliases: [{ name: 'servers', type: 'hosts', entries: ['192.0.2.10'] }],
    blocking: { ...base.blocking, lists: [{ name: 'ads', url: 'https://lists.example.com/ads' }] },
    schedules: [{ name: 'workday', start: '08:00', end: '17:00' }],
    gateways: [{ name: 'wan1', enabled: true, interface: 'eth0' }],
    gatewayGroups: [{ name: 'failover', enabled: true, members: [{ gateway: 'wan1' }] }],
    services: {
      ...base.services,
      proxy: {
        enabled: true,
        pools: [{ id: 'app', upstreams: [{ address: '192.0.2.10:80' }] }],
        sites: [{ id: 'web', enabled: true, hosts: ['www.example.com'], pool: 'app' }],
        wafProfiles: [{ id: 'strict' }],
      },
    },
    acme: { accounts: [{ id: 'le', directory: 'https://ca.example.com/dir', privateKey: 'k' }] },
    dnsProviders: [{ id: 'dns', kind: 'exec' }],
  })
}

// Each dialog refuses on save what apply would: a name outside the
// server's pattern, or one another item already holds.
describe.each<[string, Component, string, string, string]>([
  ['ZoneDialog', ZoneDialog, '#zone-name', 'lan', 'Zone lan already exists.'],
  ['AliasDialog', AliasDialog, '#alias-name', 'servers', 'Alias servers already exists.'],
  ['BlockListDialog', BlockListDialog, '#bl-name', 'ads', 'A list called ads already exists.'],
  ['ScheduleDialog', ScheduleDialog, '#sch-name', 'workday', 'Schedule workday already exists.'],
  ['ScheduleDialog', ScheduleDialog, '#sch-name', 'Work day', 'Name must be at most 31'],
  [
    'GatewayDialog',
    GatewayDialog,
    '#gw-name',
    'failover',
    'Gateway group failover already exists.',
  ],
  ['GatewayGroupDialog', GatewayGroupDialog, '#gg-name', 'wan1', 'Gateway wan1 already exists.'],
  ['PoolDialog', PoolDialog, '#pool-id', 'web', 'Site web already exists.'],
  ['SiteDialog', SiteDialog, '#site-id', 'strict', 'Profile strict already exists.'],
  ['ProfileDialog', ProfileDialog, '#waf-id', 'app', 'Pool app already exists.'],
  ['ProviderDialog', ProviderDialog, '#prov-id', 'dns', 'DNS provider dns already exists.'],
  ['ProviderDialog', ProviderDialog, '#prov-id', 'my dns', 'Name must be at most 40'],
  ['AccountDialog', AccountDialog, '#acc-id', 'le', 'Account le already exists.'],
  ['PeerDialog', PeerDialog, '#pe-name', 'laptop', 'Peer laptop already exists.'],
])('%s', (_, dialog, input, name, problem) => {
  beforeEach(() => setActivePinia(createPinia()))

  it(`refuses ${name}`, async () => {
    const config = useConfigStore()
    const cfg = draft()
    config.saved = JSON.parse(JSON.stringify(cfg)) as typeof cfg
    config.replaceDraft(cfg)
    const tunnel = must(config.findInterface('wg0'))
    const wrapper = mount(dialog, { props: { open: true, tunnel }, global: { stubs } })
    await flushPromises()
    await wrapper.get(input).setValue(name)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain(problem)
    expect(config.dirty).toBe(false)
    expect(wrapper.emitted('update:open')).toBeUndefined()
  })
})
