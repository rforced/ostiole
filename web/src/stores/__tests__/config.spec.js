import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import { useToastStore } from '@/stores/toast'

/** A draft with a zone that everything else hangs off. */
function draft() {
  return {
    version: 3,
    zones: [{ name: 'lan' }, { name: 'dmz' }, { name: 'wan', external: true }],
    interfaces: [
      { name: 'eth1', zone: 'lan' },
      { name: 'eth0', zone: 'wan' },
    ],
    rules: [
      { id: 'r1', zone: 'dmz' },
      { id: 'r2', zone: 'lan', destZone: 'dmz' },
      { id: 'r3', zone: 'lan' },
    ],
    nat: {
      outbound: {
        mode: 'hybrid',
        rules: [
          { id: 'o1', zone: 'dmz' },
          { id: 'o2', zone: 'wan' },
        ],
      },
      portForwards: [{ id: 'pf1', zone: 'dmz' }],
      oneToOne: [{ id: 'n1', zone: 'wan' }],
    },
  }
}

describe('config store zones', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('blocks deletion only on interfaces', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    expect(config.zoneInterfaces('lan')).toEqual(['eth1'])
    // dmz is used by rules and NAT, but no interface lives in it, so it
    // can go.
    expect(config.zoneInterfaces('dmz')).toEqual([])
    expect(config.zoneDependents('dmz')).toEqual([
      'rule r1',
      'rule r2',
      'port forward pf1',
      'outbound NAT o1',
    ])
  })

  it('takes the rules and NAT entries with the zone', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    config.removeZone('dmz')

    expect(config.zones.map((z) => z.name)).toEqual(['lan', 'wan'])
    // r2 named dmz only as its destination; it goes too rather than
    // silently widening to every destination.
    expect(config.rules.map((r) => r.id)).toEqual(['r3'])
    expect(config.draft.nat.portForwards).toEqual([])
    expect(config.draft.nat.outbound.rules.map((o) => o.id)).toEqual(['o2'])
    // Nothing that belonged to another zone is touched.
    expect(config.draft.nat.oneToOne.map((o) => o.id)).toEqual(['n1'])
    expect(config.interfaces).toHaveLength(2)
  })

  it('deletes a zone nothing refers to', () => {
    const config = useConfigStore()
    const d = draft()
    d.zones.push({ name: 'spare' })
    config.replaceDraft(d)
    config.removeZone('spare')
    expect(config.zones.map((z) => z.name)).toEqual(['lan', 'dmz', 'wan'])
    expect(config.rules).toHaveLength(3)
  })
})

/** A draft where an interface is named by everything that can name one. */
function wired() {
  return {
    version: 3,
    zones: [{ name: 'lan' }, { name: 'wan', external: true }],
    interfaces: [
      { name: 'eth0', zone: 'wan', ipv4: { mode: 'dhcp' }, ipv6: { mode: 'dhcp', prefixHint: 56 } },
      {
        name: 'eth1',
        zone: 'lan',
        ipv4: { mode: 'static', address: '10.0.0.1/24' },
        ipv6: { mode: 'delegated', delegatedFrom: 'eth0' },
      },
      { name: 'eth1.10', zone: 'lan', vlan: { id: 10, parent: 'eth1' } },
      { name: 'br0', zone: 'lan', bridge: { members: ['eth2', 'eth3'] } },
      { name: 'bond0', bond: { mode: 'active-backup', members: ['eth4'], primary: 'eth4' } },
      { name: 'wg0', zone: 'lan', wireguard: { peers: [{ name: 'alice' }] } },
    ],
    rules: [
      { id: 'r1', zone: 'lan', gateway: 'wan-gw' },
      { id: 'r2', zone: 'lan', gateway: 'failover' },
    ],
    gateways: [
      { name: 'wan-gw', interface: 'eth0' },
      { name: 'other', interface: 'eth5' },
    ],
    gatewayGroups: [
      {
        name: 'failover',
        members: [
          { gateway: 'wan-gw', tier: 1 },
          { gateway: 'other', tier: 2 },
        ],
      },
      { name: 'solo', members: [{ gateway: 'wan-gw', tier: 1 }] },
    ],
    routes: [{ id: 'rt1', interface: 'eth0', destination: '1.2.3.0/24' }],
    services: {
      dhcp: {
        enabled: true,
        servers: [{ interface: 'eth1' }, { interface: 'eth1.10' }],
        v6: [{ interface: 'eth1' }],
      },
      dns: { enabled: true, interfaces: ['eth1', 'eth1.10'] },
      ntp: { serve: true, interfaces: ['eth1'] },
    },
  }
}

describe('config store interfaces', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('lists what goes with an interface, children first', () => {
    const config = useConfigStore()
    config.replaceDraft(wired())
    expect(config.interfaceDependents('eth1')).toEqual([
      'VLAN eth1.10',
      'DHCP server on eth1.10',
      'DNS listener on eth1.10',
      'DHCP server on eth1',
      'IPv6 advertisement on eth1',
      'DNS listener on eth1',
      'time served on eth1',
    ])
    expect(config.interfaceDependents('eth0')).toEqual([
      'eth1 loses its delegated IPv6 prefix',
      'gateway wan-gw',
      'rule r1 loses its gateway',
      'group failover loses member wan-gw',
      'group solo, its only member',
      'route rt1',
    ])
    expect(config.interfaceDependents('eth2')).toEqual(['br0 loses member eth2'])
    expect(config.interfaceDependents('eth4')).toEqual(['bond bond0, its only member'])
  })

  it('removes an interface and everything that named it', () => {
    const config = useConfigStore()
    config.replaceDraft(wired())
    config.removeInterface('eth1')
    expect(config.interfaces.map((i) => i.name)).toEqual(['eth0', 'br0', 'bond0', 'wg0'])
    expect(config.draft.services.dhcp.servers).toEqual([])
    expect(config.draft.services.dhcp.v6).toEqual([])
    expect(config.draft.services.dns.interfaces).toEqual([])
    // An empty list means every inside interface, so it goes rather than
    // staying behind empty.
    expect(config.draft.services.ntp).toEqual({ serve: true })
  })

  it('drops gateways with their interface and clears what pointed at them', () => {
    const config = useConfigStore()
    config.replaceDraft(wired())
    config.removeInterface('eth0')
    expect(config.findInterface('eth1').ipv6).toEqual({ mode: 'none' })
    expect(config.gateways.map((g) => g.name)).toEqual(['other'])
    expect(config.rules.find((r) => r.id === 'r1').gateway).toBeUndefined()
    // r2 routes through the group, which still has a member.
    expect(config.rules.find((r) => r.id === 'r2').gateway).toBe('failover')
    expect(config.gatewayGroups.map((g) => g.name)).toEqual(['failover'])
    expect(config.gatewayGroups[0].members.map((m) => m.gateway)).toEqual(['other'])
    expect(config.routes).toEqual([])
  })

  it('takes a bridge or bond with its only member', () => {
    const config = useConfigStore()
    config.replaceDraft(wired())
    config.removeInterface('eth2')
    expect(config.findInterface('br0').bridge.members).toEqual(['eth3'])
    config.removeInterface('eth4')
    expect(config.findInterface('bond0')).toBeNull()
  })

  it('offers undo for a delete and for discard', () => {
    const config = useConfigStore()
    const toast = useToastStore()
    config.replaceDraft(wired())
    config.markSaved()

    config.removeRule('r1')
    expect(config.rules.map((r) => r.id)).toEqual(['r2'])
    expect(toast.toasts.at(-1).message).toBe('Deleted rule r1.')
    toast.act(toast.toasts.at(-1).id)
    expect(config.rules.map((r) => r.id)).toEqual(['r1', 'r2'])
    expect(config.dirty).toBe(false)

    config.removeRule('r2')
    config.discard()
    expect(config.dirty).toBe(false)
    expect(toast.toasts.at(-1).message).toBe('Draft discarded.')
    toast.act(toast.toasts.at(-1).id)
    expect(config.rules.map((r) => r.id)).toEqual(['r1'])
  })
})

describe('config store host overrides', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // The same label in two domains is two rows, so neither edit nor delete
  // may go by the label alone.
  it('keys a host override by its name and domain', () => {
    const config = useConfigStore()
    const toast = useToastStore()
    config.replaceDraft(draft())
    config.upsertHostOverride({ hostname: 'potato', ip: '10.0.0.5' })
    config.upsertHostOverride({ hostname: 'potato', domain: 'test', ip: '10.0.0.6' })

    const hosts = () => config.draft.services.dns.hostOverrides
    expect(hosts()).toHaveLength(2)

    config.upsertHostOverride({ hostname: 'potato', domain: 'test', ip: '10.0.0.7' }, 'potato.test')
    expect(hosts()).toHaveLength(2)
    expect(hosts()[1].ip).toBe('10.0.0.7')
    expect(hosts()[0].ip).toBe('10.0.0.5')

    config.removeHostOverride('potato.test')
    expect(hosts()).toEqual([{ hostname: 'potato', ip: '10.0.0.5' }])
    expect(toast.toasts.at(-1).message).toBe('Deleted host override potato.test.')
  })
})

describe('config store draft changes', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('maps change paths to sidebar items and rows', () => {
    const config = useConfigStore()
    config.replaceDraft(wired())
    config.changes = [
      { path: 'rules[r1].action', kind: 'changed' },
      { path: 'services.dhcp.servers[lan]', kind: 'added' },
      { path: 'interfaces[wg0].wireguard.peers[bob]', kind: 'added' },
      { path: 'system.keepRevisions', kind: 'changed' },
    ]
    expect(config.hasChanges('/firewall')).toBe(true)
    expect(config.hasChanges('/firewall/rules')).toBe(true)
    expect(config.hasChanges('/firewall/nat')).toBe(false)
    expect(config.hasChanges('/services/dhcp')).toBe(true)
    expect(config.hasChanges('/services/dns')).toBe(false)
    // A tunnel is an interface, but it lives under VPN.
    expect(config.hasChanges('/vpn')).toBe(true)
    expect(config.hasChanges('/vpn/wireguard')).toBe(true)
    expect(config.hasChanges('/vpn/tailscale')).toBe(false)
    expect(config.hasChanges('/interfaces')).toBe(false)
    expect(config.hasChanges('/system/configuration')).toBe(true)
    expect(config.hasChanges('/system/general')).toBe(false)
    expect(config.hasChanges('/')).toBe(false)

    expect(config.isChanged('rules', 'r1')).toBe(true)
    expect(config.isChanged('rules', 'r2')).toBe(false)
    expect(config.isChanged('services.dhcp.servers', 'lan')).toBe(true)
    expect(config.isChanged('interfaces[wg0].wireguard.peers', 'bob')).toBe(true)
    expect(config.isChanged('interfaces', 'wg0')).toBe(true)
  })

  // The remote backup is set up on the Configuration page, not under General.
  it('sends a change to the remote backup to the Configuration page', () => {
    const config = useConfigStore()
    config.replaceDraft(wired())
    config.changes = [{ path: 'backup.remote.bucket', kind: 'changed' }]
    expect(config.hasChanges('/system/configuration')).toBe(true)
    expect(config.hasChanges('/system/general')).toBe(false)
  })

  it('sends a change to the tailnet node to its own page', () => {
    const config = useConfigStore()
    config.replaceDraft(wired())
    config.draft.interfaces.push({
      name: 'tailscale0',
      zone: 'lan',
      enabled: true,
      ipv4: { mode: 'none' },
      ipv6: { mode: 'none' },
      tailscale: { port: 41641 },
    })
    config.changes = [{ path: 'interfaces[tailscale0].tailscale.port', kind: 'changed' }]
    expect(config.hasChanges('/vpn/tailscale')).toBe(true)
    expect(config.hasChanges('/vpn/wireguard')).toBe(false)
    expect(config.hasChanges('/interfaces')).toBe(false)
  })
})

describe('config store traffic shaping', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function shaped() {
    const d = draft()
    d.interfaces[0].shaping = { download: 50_000_000 }
    d.rules[2].priority = 'realtime'
    d.rules[2].enabled = true
    d.nat.portForwards[0].priority = 'high'
    d.nat.portForwards[0].enabled = true
    return d
  }

  it('lists the interfaces with a speed and leaves the rest alone', () => {
    const config = useConfigStore()
    config.replaceDraft(shaped())
    expect(config.shapedInterfaces.map((i) => i.name)).toEqual(['eth1'])
  })

  it('sets and clears a speed on the interface it belongs to', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    config.setShaping('eth0', { download: 200_000_000, upload: 20_000_000 })
    expect(config.findInterface('eth0').shaping.download).toBe(200_000_000)
    config.clearShaping('eth0')
    expect(config.findInterface('eth0').shaping).toBeUndefined()
    // Removing it is undoable, like every other delete.
    useToastStore().toasts[0].action.run()
    expect(config.findInterface('eth0').shaping.download).toBe(200_000_000)
  })

  it('collects everything that has been given a priority', () => {
    const config = useConfigStore()
    config.replaceDraft(shaped())
    expect(config.shapedRules.map((r) => r.id)).toEqual(['r3'])
    expect(config.shapedForwards.map((p) => p.id)).toEqual(['pf1'])
  })

  // A speed is stored on the interface but edited on its own page, so a
  // change to one has to be shown where it was made.
  it('files a shaping change under the shaping page', () => {
    const config = useConfigStore()
    config.replaceDraft(shaped())
    expect(config.sectionFor('interfaces[eth1].shaping.download')).toBe('/firewall/shaping')
    expect(config.sectionFor('interfaces[eth1].ipv4.address')).toBe('/interfaces')
  })

  it('sets and clears a busy host limit on the zone it belongs to', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    config.setBusy('lan', { connections: 200, priority: 'bulk' })
    expect(config.busyZones.map((z) => z.name)).toEqual(['lan'])
    config.clearBusy('lan')
    expect(config.busyZones).toEqual([])
    useToastStore().toasts[0].action.run()
    expect(config.busyZones[0].busy.connections).toBe(200)
  })

  // Holding a busy host back is a priority decision, so it is shown where
  // the other priorities are rather than with the rest of the zone.
  it('files a busy host change under the shaping page', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    expect(config.sectionFor('zones[lan].busy.connections')).toBe('/firewall/shaping')
    expect(config.sectionFor('zones[lan].antiLockout')).toBe('/interfaces')
  })

  // A network is an interface, but it is made and edited on Wireless.
  it('files a radio and its networks under the wireless page', () => {
    const config = useConfigStore()
    const d = draft()
    d.interfaces.push({ name: 'ap0', wireless: { radio: 'wlp3s0', ssid: 'lan' } })
    d.wireless = { country: 'US', radios: [{ name: 'wlp3s0' }] }
    config.replaceDraft(d)
    expect(config.sectionFor('wireless.radios[0].channel')).toBe('/wireless')
    expect(config.sectionFor('interfaces[ap0].wireless.ssid')).toBe('/wireless')
    expect(config.sectionFor('interfaces[eth1].ipv4.address')).toBe('/interfaces')
  })

  // A radio's networks mean nothing without it, so they go with it.
  it("takes a radio's networks with it", () => {
    const config = useConfigStore()
    const d = draft()
    d.interfaces.push({ name: 'ap0', wireless: { radio: 'wlp3s0', ssid: 'lan' } })
    d.wireless = { country: 'US', radios: [{ name: 'wlp3s0' }] }
    config.replaceDraft(d)
    expect(config.radioDependents('wlp3s0')).toEqual(['network lan on ap0'])
    config.removeRadio('wlp3s0')
    expect(config.radios).toEqual([])
    expect(config.findInterface('ap0')).toBeNull()
  })

  // The backup block is created on first use, so a draft written before
  // the setting existed can still be edited.
  it('creates the remote backup block when a field is set', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    expect(config.remoteBackup).toEqual({})
    config.setRemoteBackup({ bucket: 'router-backups' })
    config.setRemoteBackup({ enabled: true })
    expect(config.draft.backup.remote).toEqual({ bucket: 'router-backups', enabled: true })
    expect(config.remoteBackup.bucket).toBe('router-backups')
  })

  // The acme block is created on first use, like the backup one.
  it('keeps accounts, providers and certificates together', () => {
    const config = useConfigStore()
    const d = draft()
    d.system = { management: {} }
    config.replaceDraft(d)
    expect(config.acmeAccounts).toEqual([])

    config.upsertAcmeAccount({ id: 'le', directory: 'https://ca.test/dir' })
    config.upsertDnsProvider({ id: 'dns', kind: 'exec' })
    config.upsertCertificate({ id: 'router', source: 'acme', account: 'le', provider: 'dns' })
    config.setManagementCertificate('router')

    expect(config.draft.acme.accounts).toHaveLength(1)
    expect(config.accountDependents('le')).toEqual(['router'])
    expect(config.providerDependents('dns')).toEqual(['router'])
    expect(config.certificateDependents('router')).toEqual(['the web UI'])

    // Deleting the certificate the UI serves puts it back on the built-in
    // one rather than leaving a name nothing answers to.
    config.removeCertificate('router')
    expect(config.certificates).toEqual([])
    expect(config.draft.system.management.certificate).toBeUndefined()
    expect(config.accountDependents('le')).toEqual([])
  })

  // An edit that renames a certificate replaces the row rather than
  // leaving both.
  it('renames a certificate in place', () => {
    const config = useConfigStore()
    const d = draft()
    d.system = { management: {} }
    config.replaceDraft(d)
    config.upsertCertificate({ id: 'router', source: 'acme' })
    config.upsertCertificate({ id: 'edge', source: 'acme' }, 'router')
    expect(config.certificates.map((c) => c.id)).toEqual(['edge'])
  })

  it('files certificate changes under the certificates page', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    expect(config.sectionFor('certificates[router].names')).toBe('/system/certificates')
    expect(config.sectionFor('acme.accounts[le].email')).toBe('/system/certificates')
    expect(config.sectionFor('system.management.certificate')).toBe('/system/certificates')
    expect(config.sectionFor('system.management.webPort')).toBe('/system/general')
  })

  it('files NTP changes under the NTP page, and drops the block when it empties', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    expect(config.sectionFor('services.ntp.servers[0].host')).toBe('/services/time')
    expect(config.sectionFor('services.ntp.serve')).toBe('/services/time')
    config.setNTP({ serve: true, interfaces: ['eth1'] })
    expect(config.ntp).toEqual({ serve: true, interfaces: ['eth1'] })
    config.setNTP({ interfaces: [] })
    expect(config.ntp).toEqual({ serve: true })
    config.setNTP({ serve: false })
    expect(config.draft.services.ntp).toBeUndefined()
    expect(config.ntp).toEqual({})
  })
})

describe('config store reverse proxy', () => {
  beforeEach(() => setActivePinia(createPinia()))

  /** A draft with a pool, a site that uses it, and a profile. */
  function proxied() {
    const d = draft()
    d.services = {
      proxy: {
        enabled: true,
        pools: [
          { id: 'web', upstreams: [{ address: '10.0.0.2:80' }] },
          { id: 'api', upstreams: [{ address: '10.0.0.3:80' }] },
        ],
        wafProfiles: [{ id: 'strict', mode: 'block' }],
        sites: [
          {
            id: 'shop',
            enabled: true,
            hosts: ['shop.example.com'],
            pool: 'web',
            paths: [{ prefix: '/api', pool: 'api' }],
            waf: 'strict',
          },
        ],
        routes: [
          {
            id: 'mail',
            enabled: true,
            protocol: 'tcp',
            port: 993,
            upstreams: [{ address: 'm:993' }],
          },
        ],
      },
    }
    return d
  }

  it('files proxy changes under the proxy page', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    expect(config.sectionFor('services.proxy.sites[0].hosts')).toBe('/services/proxy')
    expect(config.sectionFor('services.proxy.enabled')).toBe('/services/proxy')
    expect(config.sectionFor('services.upnp.enabled')).toBe('/services/upnp')
    expect(config.sectionFor('services.dhcp.servers[0].interface')).toBe('/services/dhcp')
  })

  it('names what a pool leaves behind', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    expect(config.poolDependents('web')).toEqual(['site shop'])
    expect(config.poolDependents('api')).toEqual(['site shop path /api'])
    expect(config.profileDependents('strict')).toEqual(['site shop'])
    config.removePool('web')
    expect(config.proxy.pools.map((p) => p.id)).toEqual(['api'])
    useToastStore().toasts[0].action.run()
    expect(config.proxy.pools.map((p) => p.id)).toEqual(['web', 'api'])
  })

  it('renames a site in place', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    config.upsertSite({ id: 'store', enabled: true, hosts: ['s.example.com'], pool: 'web' }, 'shop')
    expect(config.proxy.sites.map((s) => s.id)).toEqual(['store'])
  })

  it('creates the proxy block on first use', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    expect(config.proxy).toEqual({ enabled: false })
    config.upsertPool({ id: 'web', upstreams: [{ address: '10.0.0.2:80' }] })
    expect(config.proxy.pools).toHaveLength(1)
  })

  it('excludes a rule once, and undo takes it back out', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    config.addExclusion('strict', { rule: '942100' })
    expect(config.proxy.wafProfiles[0].exclusions).toEqual([{ rule: '942100' }])
    // The same rule twice is the same exclusion.
    config.addExclusion('strict', { rule: '942100' })
    expect(config.proxy.wafProfiles[0].exclusions).toHaveLength(1)
    // On a path it is a different one.
    config.addExclusion('strict', { rule: '942100', path: '/wp-admin' })
    expect(config.proxy.wafProfiles[0].exclusions).toHaveLength(2)
    const toasts = useToastStore().toasts
    toasts[toasts.length - 1].action.run()
    expect(config.proxy.wafProfiles[0].exclusions).toEqual([{ rule: '942100' }])
  })

  it('drops a route by id', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    config.removeProxyRoute('mail')
    expect(config.proxy.routes).toEqual([])
  })
})

describe('config store DNS blocking exceptions', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function blocked() {
    const d = draft()
    d.blocking = { enabled: true, enforce: {}, deny: ['tracker.example.net'] }
    return d
  }

  it('puts a name on a list and takes it off again without a trace', () => {
    const config = useConfigStore()
    config.replaceDraft(blocked())
    config.markSaved()
    config.setException('ads.example.com', 'allow', true)
    expect(config.blocking.allow).toEqual(['ads.example.com'])
    // Twice is still once.
    config.setException('Ads.Example.com.', 'allow', true)
    expect(config.blocking.allow).toEqual(['ads.example.com'])
    config.setException('ads.example.com', 'allow', false)
    expect(config.draft.blocking).not.toHaveProperty('allow')
    expect(config.dirty).toBe(false)
  })

  it('takes a name off one list as it goes on the other', () => {
    const config = useConfigStore()
    config.replaceDraft(blocked())
    config.setException('Tracker.Example.net', 'allow', true)
    expect(config.blocking.allow).toEqual(['tracker.example.net'])
    expect(config.draft.blocking).not.toHaveProperty('deny')
    config.setException('tracker.example.net', 'deny', true)
    expect(config.blocking.deny).toEqual(['tracker.example.net'])
    expect(config.draft.blocking).not.toHaveProperty('allow')
  })
})

describe('config store wake on LAN', () => {
  beforeEach(() => setActivePinia(createPinia()))

  /** Two machines on the LAN, one on a VLAN of it, and a cron that wakes one. */
  function sleepers() {
    const d = draft()
    d.interfaces.push({ name: 'eth1.20', zone: 'lan', vlan: { parent: 'eth1', id: 20 } })
    d.services = {
      dhcp: { enabled: false },
      dns: { enabled: false },
      wol: {
        devices: [
          { id: 'wol-nas', interface: 'eth1', mac: 'aa:bb:cc:00:00:01', description: 'NAS' },
          { id: 'wol-pc', interface: 'eth1.20', mac: 'aa:bb:cc:00:00:02' },
        ],
      },
    }
    d.crons = [
      { id: 'c1', kind: 'wake', device: 'wol-nas', description: 'Morning NAS' },
      { id: 'c2', kind: 'wake', device: 'wol-pc' },
      { id: 'c3', kind: 'backup', directory: '/b' },
    ]
    return d
  }

  it('adds and edits a device, and drops the block when the last one goes', () => {
    const config = useConfigStore()
    // The server always writes these two blocks.
    config.replaceDraft({
      ...draft(),
      services: { dhcp: { enabled: false }, dns: { enabled: false } },
    })
    config.markSaved()
    config.upsertWoLDevice({ id: 'wol-a', interface: 'eth1', mac: 'aa:bb:cc:00:00:09' })
    expect(config.wolDevices).toHaveLength(1)
    expect(config.sectionFor('services.wol.devices[wol-a].mac')).toBe('/services/wol')
    config.upsertWoLDevice(
      { id: 'wol-a', interface: 'eth1', mac: 'aa:bb:cc:00:00:09', description: 'NAS' },
      'wol-a',
    )
    expect(config.wolDevices).toEqual([
      { id: 'wol-a', interface: 'eth1', mac: 'aa:bb:cc:00:00:09', description: 'NAS' },
    ])
    config.removeWoLDevice(config.wolDevices[0])
    expect(config.draft.services).not.toHaveProperty('wol')
    expect(config.dirty).toBe(false)
  })

  it('takes a device wake cron jobs with it', () => {
    const config = useConfigStore()
    const toast = useToastStore()
    config.replaceDraft(sleepers())
    expect(config.wolDeviceDependents('wol-nas')).toEqual(['cron job Morning NAS'])
    config.removeWoLDevice(config.wolDevices[0])
    expect(toast.toasts.at(-1).message).toBe('Deleted NAS.')
    expect(config.wolDevices.map((d) => d.id)).toEqual(['wol-pc'])
    expect(config.crons.map((c) => c.id)).toEqual(['c2', 'c3'])
  })

  it('goes with its interface, and a VLAN child takes its own', () => {
    const config = useConfigStore()
    config.replaceDraft(sleepers())
    expect(config.interfaceDependents('eth1')).toEqual([
      'VLAN eth1.20',
      'Wake on LAN device aa:bb:cc:00:00:02',
      'cron job c2',
      'Wake on LAN device NAS',
      'cron job Morning NAS',
    ])
    config.removeInterface('eth1')
    expect(config.draft.services).not.toHaveProperty('wol')
    expect(config.crons.map((c) => c.id)).toEqual(['c3'])
  })
})
