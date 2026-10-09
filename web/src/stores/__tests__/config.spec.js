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
      'outbound NAT o1',
      'port forward pf1',
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

  it('keeps the defended zones in step with a rename and a delete', () => {
    const config = useConfigStore()
    const d = draft()
    d.zones.push({ name: 'spare' })
    d.protection = { zones: ['wan', 'dmz'], synFlood: { rate: 30, unit: 'second' } }
    config.replaceDraft(d)

    config.upsertZone({ name: 'guest' }, 'dmz')
    expect(config.draft.protection.zones).toEqual(['wan', 'guest'])
    config.removeZone('guest')
    expect(config.draft.protection.zones).toEqual(['wan'])
    // The last one named goes with the list: empty means every external zone.
    config.setProtectedZones(['spare'])
    config.removeZone('spare')
    expect(config.draft.protection).toEqual({ synFlood: { rate: 30, unit: 'second' } })
  })

  it('says what protection goes back to when a zone goes', () => {
    const config = useConfigStore()
    const d = draft()
    d.protection = { zones: ['wan', 'dmz'] }
    d.services = { proxy: { access: [{ id: 'a1', zone: 'dmz', action: 'allow' }] } }
    config.replaceDraft(d)
    expect(config.zoneDependents('dmz')).toEqual([
      'rule r1',
      'rule r2',
      'outbound NAT o1',
      'port forward pf1',
      'protection',
      'proxy access rule a1',
    ])
    expect(config.zoneDependents('lan')).toEqual(['rule r2', 'rule r3'])

    config.setProtectedZones(['dmz'])
    expect(config.zoneDependents('dmz').slice(4)).toEqual([
      'protection, back to every external zone',
      'proxy access rule a1',
    ])
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

  it('takes a bridge of VLANs with their trunk, and what names the bridge', () => {
    const config = useConfigStore()
    const d = wired()
    d.interfaces.push(
      { name: 'eth1.20', zone: 'lan', vlan: { id: 20, parent: 'eth1' } },
      { name: 'br1', zone: 'lan', bridge: { members: ['eth1.10', 'eth1.20'] } },
    )
    d.services.dhcp.servers.push({ interface: 'br1' })
    d.gateways.push({ name: 'inside', interface: 'br1' })
    d.routes.push({ id: 'rt2', interface: 'br1', destination: '192.0.2.0/24' })
    config.replaceDraft(d)
    expect(config.interfaceDependents('eth1')).toEqual([
      'VLAN eth1.10',
      'DHCP server on eth1.10',
      'DNS listener on eth1.10',
      'VLAN eth1.20',
      'bridge br1, left without members',
      'DHCP server on br1',
      'gateway inside',
      'route rt2',
      'DHCP server on eth1',
      'IPv6 advertisement on eth1',
      'DNS listener on eth1',
      'time served on eth1',
    ])
    config.removeInterface('eth1')
    expect(config.interfaces.map((i) => i.name)).toEqual(['eth0', 'br0', 'bond0', 'wg0'])
    expect(config.draft.services.dhcp.servers).toEqual([])
    expect(config.gateways.map((g) => g.name)).toEqual(['wan-gw', 'other'])
    expect(config.routes.map((r) => r.id)).toEqual(['rt1'])
  })

  it('keeps a bridge of VLANs that has another member', () => {
    const config = useConfigStore()
    const d = wired()
    d.interfaces.push(
      { name: 'eth1.20', zone: 'lan', vlan: { id: 20, parent: 'eth1' } },
      { name: 'br1', zone: 'lan', bridge: { members: ['eth1.10', 'eth1.20', 'eth7'] } },
    )
    d.services.dhcp.servers.push({ interface: 'br1' })
    config.replaceDraft(d)
    expect(config.interfaceDependents('eth1')).toEqual([
      'VLAN eth1.10',
      'br1 loses member eth1.10',
      'DHCP server on eth1.10',
      'DNS listener on eth1.10',
      'VLAN eth1.20',
      'br1 loses member eth1.20',
      'DHCP server on eth1',
      'IPv6 advertisement on eth1',
      'DNS listener on eth1',
      'time served on eth1',
    ])
    config.removeInterface('eth1')
    expect(config.findInterface('br1').bridge.members).toEqual(['eth7'])
    expect(config.draft.services.dhcp.servers).toEqual([{ interface: 'br1' }])
  })

  it('takes a gateway group whose every gateway goes with the interface', () => {
    const config = useConfigStore()
    config.replaceDraft({
      version: 3,
      zones: [{ name: 'wan', external: true }],
      interfaces: [{ name: 'eth0', zone: 'wan' }],
      gateways: [
        { name: 'v4', interface: 'eth0' },
        { name: 'v6', interface: 'eth0' },
      ],
      gatewayGroups: [
        {
          name: 'both',
          members: [
            { gateway: 'v4', tier: 1 },
            { gateway: 'v6', tier: 2 },
          ],
        },
      ],
      rules: [{ id: 'r1', zone: 'wan', gateway: 'both' }],
    })
    expect(config.interfaceDependents('eth0')).toEqual([
      'gateway v4',
      'gateway v6',
      'group both, left without members',
      'rule r1 loses its gateway',
    ])
    expect(config.gatewayDependents('v4')).toEqual(['group both loses member v4'])
    config.removeInterface('eth0')
    expect(config.gatewayGroups).toEqual([])
    expect(config.rules[0].gateway).toBeUndefined()
  })

  it('takes an interface out of UPnP and the certificates that cover its address', () => {
    const config = useConfigStore()
    const d = wired()
    d.interfaces.push({ name: 'eth6', zone: 'wan' })
    d.services.upnp = {
      enabled: true,
      igd: true,
      externalInterface: 'eth0',
      interfaces: ['eth1', 'br0'],
    }
    d.system = { management: { certificate: 'edge' } }
    d.certificates = [
      { id: 'edge', enabled: true, source: 'acme', interfaceAddresses: ['eth0'] },
      { id: 'both', enabled: true, source: 'acme', interfaceAddresses: ['eth0', 'eth6'] },
      {
        id: 'named',
        enabled: true,
        source: 'acme',
        names: ['router.example.test'],
        interfaceAddresses: ['eth0'],
      },
    ]
    config.replaceDraft(d)
    expect(config.interfaceDependents('eth0')).toEqual([
      'eth1 loses its delegated IPv6 prefix',
      'UPnP, switched off without its external interface',
      'certificate edge, which covers only eth0',
      'the web UI goes back to the built-in certificate',
      'certificate both loses the address of eth0',
      'certificate named loses the address of eth0',
      'gateway wan-gw',
      'rule r1 loses its gateway',
      'group failover loses member wan-gw',
      'group solo, its only member',
      'route rt1',
    ])
    expect(config.interfaceDependents('br0')).toEqual(['UPnP clients on br0'])

    config.removeInterface('eth0')
    // UPnP cannot run without the interface it opens ports on.
    expect(config.draft.services.upnp).toEqual({
      enabled: false,
      igd: true,
      interfaces: ['eth1', 'br0'],
    })
    expect(config.certificates.map((c) => c.id)).toEqual(['both', 'named'])
    expect(config.certificates[0].interfaceAddresses).toEqual(['eth6'])
    expect(config.certificates[1].interfaceAddresses).toBeUndefined()
    expect(config.draft.system.management.certificate).toBeUndefined()
  })

  // An empty list means every inside interface, which is not for a delete
  // to decide.
  it('switches UPnP off with the only interface clients ask from', () => {
    const config = useConfigStore()
    const d = wired()
    d.services.upnp = { enabled: true, pcp: true, externalInterface: 'eth0', interfaces: ['br0'] }
    config.replaceDraft(d)
    expect(config.interfaceDependents('br0')).toEqual([
      'UPnP, switched off rather than answer every inside interface',
    ])
    config.removeInterface('br0')
    expect(config.draft.services.upnp).toEqual({
      enabled: false,
      pcp: true,
      externalInterface: 'eth0',
    })

    // Off already, it only loses the name.
    d.services.upnp = { enabled: false, externalInterface: 'eth0', interfaces: ['br0'] }
    config.replaceDraft(d)
    expect(config.interfaceDependents('br0')).toEqual(['UPnP clients on br0'])
    config.removeInterface('eth0')
    expect(config.draft.services.upnp).toEqual({ enabled: false, interfaces: ['br0'] })
  })

  // Either VLAN alone would leave UPnP and the certificate the other one.
  it('weighs UPnP and certificates against every interface a delete takes', () => {
    const config = useConfigStore()
    const d = wired()
    d.interfaces.push({ name: 'eth1.20', zone: 'lan', vlan: { id: 20, parent: 'eth1' } })
    d.services.upnp = {
      enabled: true,
      externalInterface: 'eth0',
      interfaces: ['eth1.10', 'eth1.20'],
    }
    d.certificates = [
      { id: 'inside', enabled: true, source: 'acme', interfaceAddresses: ['eth1.10', 'eth1.20'] },
      {
        id: 'named',
        enabled: true,
        source: 'acme',
        names: ['router.example.test'],
        interfaceAddresses: ['eth0', 'eth1.10', 'eth1.20'],
      },
    ]
    config.replaceDraft(d)
    expect(config.interfaceDependents('eth1')).toEqual([
      'VLAN eth1.10',
      'DHCP server on eth1.10',
      'DNS listener on eth1.10',
      'VLAN eth1.20',
      'DHCP server on eth1',
      'IPv6 advertisement on eth1',
      'DNS listener on eth1',
      'time served on eth1',
      'UPnP, switched off rather than answer every inside interface',
      'certificate inside, which covers only eth1.10 and eth1.20',
      'certificate named loses the address of eth1.10',
      'certificate named loses the address of eth1.20',
    ])
    config.removeInterface('eth1')
    expect(config.draft.services.upnp).toEqual({ enabled: false, externalInterface: 'eth0' })
    expect(config.certificates.map((c) => c.id)).toEqual(['named'])
    expect(config.certificates[0].interfaceAddresses).toEqual(['eth0'])

    // A bridge of the two goes with them, and UPnP with the bridge.
    d.interfaces.push({ name: 'br9', zone: 'lan', bridge: { members: ['eth1.10', 'eth1.20'] } })
    d.services.upnp.interfaces = ['br9']
    config.replaceDraft(d)
    expect(config.interfaceDependents('eth1')).toContain(
      'UPnP, switched off rather than answer every inside interface',
    )
    config.removeInterface('eth1')
    expect(config.findInterface('br9')).toBeNull()
    expect(config.draft.services.upnp).toEqual({ enabled: false, externalInterface: 'eth0' })
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

  // Removing several items at once is one change to take back, not one each.
  it('folds removers run inside one undoable into it', () => {
    const config = useConfigStore()
    const toast = useToastStore()
    config.replaceDraft(wired())
    const before = JSON.parse(JSON.stringify(config.draft))

    config.undoable('Removed 2 unused items.', () => {
      config.removeRule('r1')
      config.removeRule('r2')
    })
    expect(config.rules).toEqual([])
    expect(toast.toasts.map((t) => t.message)).toEqual(['Removed 2 unused items.'])
    toast.act(toast.toasts[0].id)
    expect(config.draft).toEqual(before)

    config.removeRule('r1')
    expect(toast.toasts.at(-1).message).toBe('Deleted rule r1.')
  })

  // Half a bulk removal with no toast would have no way back.
  it('puts the draft back when a run inside one undoable throws', () => {
    const config = useConfigStore()
    const toast = useToastStore()
    config.replaceDraft(wired())
    const before = JSON.parse(JSON.stringify(config.draft))

    expect(() =>
      config.undoable('Removed 2 unused items.', () => {
        config.removeRule('r1')
        throw new Error('no such rule')
      }),
    ).toThrow('no such rule')
    expect(config.draft).toEqual(before)
    expect(toast.toasts).toEqual([])

    config.removeRule('r1')
    expect(toast.toasts.map((t) => t.message)).toEqual(['Deleted rule r1.'])
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
      { path: 'system.keepDriveReadings', kind: 'changed' },
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
    expect(config.hasChanges('/diagnostics/drives')).toBe(true)
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
    expect(config.draft.dnsProviders).toEqual([{ id: 'dns', kind: 'exec' }])
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

  // A site or the web UI left on the old id would name nothing.
  it('follows a renamed certificate in its sites and the management port', () => {
    const config = useConfigStore()
    const d = draft()
    d.system = { management: { certificate: 'router' } }
    d.certificates = [
      { id: 'router', enabled: true, source: 'acme', names: ['router.example.test'] },
    ]
    d.services = {
      proxy: {
        enabled: true,
        pools: [{ id: 'web', upstreams: [{ address: '10.0.0.2:80' }] }],
        sites: [
          {
            id: 'shop',
            enabled: true,
            hosts: ['shop.example.com'],
            pool: 'web',
            certificate: 'router',
          },
          { id: 'blog', enabled: true, hosts: ['blog.example.com'], pool: 'web' },
        ],
      },
    }
    config.replaceDraft(d)
    config.markSaved()

    const renamed = { ...d.certificates[0], id: 'edge' }
    config.upsertCertificate(renamed, 'router')
    expect(config.certificates.map((c) => c.id)).toEqual(['edge'])
    expect(config.draft.system.management.certificate).toBe('edge')
    expect(config.proxy.sites.map((s) => s.certificate)).toEqual(['edge', undefined])

    // An edit has no undo of its own; discarding the draft puts the old id
    // back everywhere, and undoing that brings the rename back whole.
    config.discard()
    expect(config.certificates.map((c) => c.id)).toEqual(['router'])
    expect(config.draft.system.management.certificate).toBe('router')
    expect(config.proxy.sites.map((s) => s.certificate)).toEqual(['router', undefined])
    useToastStore().toasts.at(-1).action.run()
    expect(config.draft.system.management.certificate).toBe('edge')
    expect(config.proxy.sites.map((s) => s.certificate)).toEqual(['edge', undefined])
  })

  it('files certificate changes under the certificates page', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    expect(config.sectionFor('certificates[router].names')).toBe('/system/certificates')
    expect(config.sectionFor('acme.accounts[le].email')).toBe('/system/certificates')
    expect(config.sectionFor('system.management.certificate')).toBe('/system/certificates')
    expect(config.sectionFor('system.management.webPort')).toBe('/system/general')
    expect(config.sectionFor('dnsProviders[cf].domains[0]')).toBe('/system/dns-providers')
  })

  it('files a peer log change under its tunnel page', () => {
    const config = useConfigStore()
    const d = draft()
    config.replaceDraft(d)
    expect(config.sectionFor('vpn.wireguardLog.entries')).toBe('/vpn/wireguard')
    expect(config.sectionFor('vpn.tailscaleLog.entries')).toBe('/vpn/tailscale')
    d.vpn = { tailscaleLog: { entries: 1000 } }
    config.replaceDraft(d)
    expect(config.sectionFor('vpn')).toBe('/vpn/tailscale')
    d.vpn.wireguardLog = { entries: 1000 }
    config.replaceDraft(d)
    expect(config.sectionFor('vpn')).toBe('/vpn')
  })

  // The connection limit lives under system but is edited on Protection.
  it('files the connection limit under the protection page', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    expect(config.sectionFor('system.conntrackMax')).toBe('/firewall/protection')
  })

  // The last provider takes the list with it, so a page that only looked
  // leaves no empty list behind to mark the draft changed.
  it('drops the provider list when the last one goes', () => {
    const config = useConfigStore()
    config.replaceDraft(draft())
    config.upsertDnsProvider({ id: 'cf', kind: 'cloudflare' })
    config.removeDnsProvider('cf')
    expect(config.draft).not.toHaveProperty('dnsProviders')
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

  it('names the sites and paths that use a pool', () => {
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

  it('leaves the sites a deleted profile inspected uninspected', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    config.removeProfile('strict')
    expect(config.proxy.wafProfiles).toEqual([])
    expect(config.proxy.sites[0].waf).toBeUndefined()
    expect(config.proxy.sites[0].pool).toBe('web')
    useToastStore().toasts[0].action.run()
    expect(config.proxy.sites[0].waf).toBe('strict')
  })

  // A site without a certificate serves the built-in one, as the web UI does.
  it('puts the sites a deleted certificate served back on the built-in one', () => {
    const config = useConfigStore()
    const d = proxied()
    d.system = { management: { certificate: 'edge' } }
    d.certificates = [
      { id: 'edge', enabled: true, source: 'acme', names: ['shop.example.com'] },
      { id: 'other', enabled: true, source: 'acme', names: ['blog.example.com'] },
    ]
    d.services.proxy.sites[0].certificate = 'edge'
    d.services.proxy.sites.push({
      id: 'blog',
      enabled: true,
      hosts: ['blog.example.com'],
      pool: 'web',
      certificate: 'other',
    })
    config.replaceDraft(d)
    expect(config.certificateDependents('edge')).toEqual(['the web UI', 'site shop'])
    expect(config.certificateDependents('other')).toEqual(['site blog'])

    config.removeCertificate('edge')
    expect(config.certificates.map((c) => c.id)).toEqual(['other'])
    expect(config.draft.system.management.certificate).toBeUndefined()
    expect(config.proxy.sites.map((s) => s.certificate)).toEqual([undefined, 'other'])
  })

  it('renames a site in place', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    config.upsertSite({ id: 'store', enabled: true, hosts: ['s.example.com'], pool: 'web' }, 'shop')
    expect(config.proxy.sites.map((s) => s.id)).toEqual(['store'])
  })

  it('follows a renamed pool in the sites and paths that send to it', () => {
    const config = useConfigStore()
    const d = proxied()
    d.services.proxy.sites.push({
      id: 'blog',
      enabled: true,
      hosts: ['blog.example.com'],
      pool: 'api',
      paths: [{ prefix: '/media', pool: 'web' }],
    })
    config.replaceDraft(d)
    config.markSaved()

    config.upsertPool({ id: 'front', upstreams: [{ address: '10.0.0.2:80' }] }, 'web')
    expect(config.proxy.pools.map((p) => p.id)).toEqual(['front', 'api'])
    expect(config.proxy.sites.map((s) => s.pool)).toEqual(['front', 'api'])
    expect(config.proxy.sites.map((s) => s.paths[0].pool)).toEqual(['api', 'front'])

    config.discard()
    expect(config.proxy.pools.map((p) => p.id)).toEqual(['web', 'api'])
    expect(config.proxy.sites.map((s) => s.pool)).toEqual(['web', 'api'])
    expect(config.proxy.sites.map((s) => s.paths[0].pool)).toEqual(['api', 'web'])
    useToastStore().toasts.at(-1).action.run()
    expect(config.proxy.sites.map((s) => s.pool)).toEqual(['front', 'api'])
    expect(config.proxy.sites.map((s) => s.paths[0].pool)).toEqual(['api', 'front'])
  })

  it('follows a renamed WAF profile in the sites it inspects', () => {
    const config = useConfigStore()
    const d = proxied()
    d.services.proxy.sites.push({
      id: 'blog',
      enabled: true,
      hosts: ['blog.example.com'],
      pool: 'web',
    })
    config.replaceDraft(d)
    config.markSaved()

    config.upsertProfile({ id: 'tight', mode: 'block' }, 'strict')
    expect(config.proxy.wafProfiles.map((w) => w.id)).toEqual(['tight'])
    expect(config.proxy.sites.map((s) => s.waf)).toEqual(['tight', undefined])

    config.discard()
    expect(config.proxy.wafProfiles.map((w) => w.id)).toEqual(['strict'])
    expect(config.proxy.sites.map((s) => s.waf)).toEqual(['strict', undefined])
    useToastStore().toasts.at(-1).action.run()
    expect(config.proxy.sites.map((s) => s.waf)).toEqual(['tight', undefined])
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

  it('says a rule is excluded already, and tells one variable from the whole rule', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    config.addExclusion('strict', { rule: '942100', target: 'ARGS:q' })
    config.addExclusion('strict', { rule: '942100' })
    expect(config.proxy.wafProfiles[0].exclusions).toHaveLength(2)
    config.addExclusion('strict', { rule: '942100', target: 'ARGS:q', description: 'Search' })
    expect(config.proxy.wafProfiles[0].exclusions).toHaveLength(2)
    const last = useToastStore().toasts.at(-1)
    expect(last.message).toBe('Rule 942100 for ARGS:q is excluded already.')
    expect(last.action).toBeNull()
  })

  it('edits and deletes an exclusion by its place, and undo puts it back', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    config.addExclusion('strict', { rule: '942100' })
    config.addExclusion('strict', { rule: '941100', path: '/api' })
    config.updateExclusion('strict', 1, { rule: '941100', path: '/api/v2' })
    expect(config.proxy.wafProfiles[0].exclusions[1]).toEqual({ rule: '941100', path: '/api/v2' })
    config.removeExclusion('strict', 0)
    expect(config.proxy.wafProfiles[0].exclusions).toEqual([{ rule: '941100', path: '/api/v2' }])
    expect(useToastStore().toasts.at(-1).message).toBe('Deleted the exclusion of rule 942100.')
    // The last one takes the list with it, as a saved profile has none.
    config.removeExclusion('strict', 0)
    expect(config.proxy.wafProfiles[0]).not.toHaveProperty('exclusions')
    useToastStore().toasts.at(-1).action.run()
    expect(config.proxy.wafProfiles[0].exclusions).toEqual([{ rule: '941100', path: '/api/v2' }])
  })

  it('drops a route by id', () => {
    const config = useConfigStore()
    config.replaceDraft(proxied())
    config.removeProxyRoute('mail')
    expect(config.proxy.routes).toEqual([])
  })

  // An access rule belongs to its zone and names aliases, so it follows a
  // rename and goes with a delete, as a firewall rule does.
  it('keeps the access list in step with zones and aliases', () => {
    const d = proxied()
    d.aliases = [{ name: 'home', type: 'hosts', entries: ['198.51.100.0/24'] }]
    d.services.proxy.access = [
      { id: 'a1', enabled: true, zone: 'dmz', action: 'accept', ports: ['https'], source: {} },
      {
        id: 'a2',
        enabled: true,
        zone: 'wan',
        action: 'accept',
        ports: ['https'],
        source: { alias: 'home' },
      },
    ]
    d.services.proxy.sites[0].allowFrom = ['home', '10.0.0.0/8']
    const config = useConfigStore()
    config.replaceDraft(d)
    expect(config.zoneDependents('dmz')).toContain('proxy access rule a1')
    expect(config.aliasReferences('home')).toEqual(['proxy access rule a2', 'site shop allow from'])

    config.upsertZone({ name: 'outside', external: true }, 'wan')
    config.upsertAlias({ name: 'house', type: 'hosts', entries: ['198.51.100.0/24'] }, 'home')
    expect(config.proxy.access[1]).toMatchObject({ zone: 'outside', source: { alias: 'house' } })
    expect(config.proxy.sites[0].allowFrom).toEqual(['house', '10.0.0.0/8'])

    config.removeZone('dmz')
    expect(config.proxy.access.map((a) => a.id)).toEqual(['a2'])
  })

  it('moves an access rule among those of its zone', () => {
    const d = proxied()
    const line = (id, zone) => ({ id, enabled: true, zone, action: 'accept', ports: ['https'] })
    d.services.proxy.access = [line('a', 'wan'), line('b', 'lan'), line('c', 'wan')]
    const config = useConfigStore()
    config.replaceDraft(d)
    config.moveProxyAccess('c', -1)
    expect(config.proxy.access.map((a) => a.id)).toEqual(['c', 'b', 'a'])
    // The first of its zone goes nowhere.
    config.moveProxyAccess('c', -1)
    expect(config.proxy.access.map((a) => a.id)).toEqual(['c', 'b', 'a'])
    config.removeProxyAccess('b')
    expect(config.proxy.access.map((a) => a.id)).toEqual(['c', 'a'])
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
      { id: 'c3', kind: 'backup' },
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

describe('config store dynamic DNS', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function kept() {
    const d = draft()
    d.dnsProviders = [
      { id: 'cf', kind: 'cloudflare', settings: { token: 't' }, domains: ['example.com'] },
      { id: 'home', kind: 'cloudflare', settings: { token: 't' }, domains: ['home.example.com'] },
    ]
    d.services = {
      dhcp: { enabled: false },
      dns: { enabled: false },
      ddns: {
        records: [
          { id: 'ddns-www', enabled: true, name: 'www.example.com', interface: 'eth0', ipv4: true },
          {
            id: 'ddns-nas',
            enabled: true,
            name: 'nas.home.example.com',
            interface: 'eth1',
            ipv6: true,
          },
        ],
      },
    }
    return d
  }

  it('adds and edits a record, and drops the block when the last one goes', () => {
    const config = useConfigStore()
    config.replaceDraft({
      ...draft(),
      services: { dhcp: { enabled: false }, dns: { enabled: false } },
    })
    config.markSaved()
    const r = { id: 'ddns-a', enabled: true, name: 'a.example.com', interface: 'eth0', ipv4: true }
    config.upsertDdnsRecord(r)
    expect(config.sectionFor('services.ddns.records[ddns-a].name')).toBe('/services/ddns')
    config.upsertDdnsRecord({ ...r, description: 'Office' }, 'ddns-a')
    expect(config.ddnsRecords).toEqual([{ ...r, description: 'Office' }])
    config.removeDdnsRecord(config.ddnsRecords[0])
    expect(config.draft.services).not.toHaveProperty('ddns')
    expect(config.dirty).toBe(false)
  })

  // A provider is in use by the records under the longest domain it holds.
  it('counts a record against the provider that writes it', () => {
    const config = useConfigStore()
    config.replaceDraft(kept())
    expect(config.providerDependents('cf')).toEqual(['www.example.com'])
    expect(config.providerDependents('home')).toEqual(['nas.home.example.com'])
  })

  it('goes with its interface', () => {
    const config = useConfigStore()
    config.replaceDraft(kept())
    expect(config.interfaceDependents('eth1')).toContain('dynamic DNS record nas.home.example.com')
    config.removeInterface('eth1')
    expect(config.ddnsRecords.map((r) => r.id)).toEqual(['ddns-www'])
  })
})

/** A draft with a tunnel whose peers rules and the proxy's access list name. */
function tunnelDraft() {
  return {
    version: 11,
    zones: [{ name: 'lan' }, { name: 'wg0' }],
    interfaces: [
      { name: 'eth1', zone: 'lan' },
      {
        name: 'wg0',
        zone: 'wg0',
        wireguard: {
          peers: [
            { name: 'laptop', allowedIps: ['10.66.0.2/32'] },
            { name: 'phone', allowedIps: ['10.66.0.3/32'] },
          ],
        },
      },
    ],
    rules: [
      { id: 'r-laptop', zone: 'wg0', source: { peer: 'wg0/laptop' }, destination: {} },
      { id: 'r-to-phone', zone: 'lan', source: {}, destination: { peer: 'wg0/phone' } },
      { id: 'r-lan', zone: 'lan', source: {}, destination: {} },
    ],
    services: {
      proxy: { access: [{ id: 'a1', zone: 'wg0', source: { peer: 'wg0/laptop' } }] },
    },
  }
}

describe('config store WireGuard peers', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('renames a peer everywhere a rule names it', () => {
    const config = useConfigStore()
    config.replaceDraft(tunnelDraft())
    const laptop = config.findInterface('wg0').wireguard.peers[0]
    config.upsertPeer('wg0', { ...laptop, name: 'notebook' }, 'laptop')
    expect(config.rules[0].source.peer).toBe('wg0/notebook')
    expect(config.draft.services.proxy.access[0].source.peer).toBe('wg0/notebook')
    expect(config.rules[1].destination.peer).toBe('wg0/phone')
  })

  it('takes the rules that name a peer with it', () => {
    const config = useConfigStore()
    config.replaceDraft(tunnelDraft())
    expect(config.peerDependents('wg0', 'laptop')).toEqual([
      'rule r-laptop',
      'proxy access rule a1',
    ])
    config.removePeer('wg0', 'laptop')
    // A rule that lost its peer would match everyone, so it goes.
    expect(config.rules.map((r) => r.id)).toEqual(['r-to-phone', 'r-lan'])
    expect(config.draft.services.proxy.access).toEqual([])
  })

  it('takes the rules that name any of its peers with a tunnel', () => {
    const config = useConfigStore()
    config.replaceDraft(tunnelDraft())
    expect(config.interfaceDependents('wg0')).toEqual(
      expect.arrayContaining(['rule r-laptop', 'rule r-to-phone', 'proxy access rule a1']),
    )
    config.removeTunnel('wg0')
    expect(config.rules.map((r) => r.id)).toEqual(['r-lan'])
    expect(config.draft.services.proxy.access).toEqual([])
  })
})

describe('config store aliases', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function enforced() {
    return {
      version: 14,
      zones: [],
      interfaces: [],
      rules: [],
      aliases: [
        { name: 'doh', type: 'hosts', entries: ['9.9.9.9'] },
        { name: 'kids', type: 'hosts', entries: ['192.168.1.20'] },
        { name: 'cloud', type: 'hosts', entries: ['198.51.100.0/24'] },
      ],
      blocking: {
        enforce: { dohAlias: 'doh', exemptClients: 'kids', exemptDestinations: 'cloud' },
      },
    }
  }

  it('renames an alias in the DNS blocking settings too', () => {
    const config = useConfigStore()
    config.replaceDraft(enforced())
    config.upsertAlias({ name: 'doh_servers', type: 'hosts', entries: ['9.9.9.9'] }, 'doh')
    config.upsertAlias({ name: 'grown_ups', type: 'hosts', entries: ['192.168.1.20'] }, 'kids')
    config.upsertAlias({ name: 'work_cloud', type: 'hosts', entries: ['198.51.100.0/24'] }, 'cloud')
    expect(config.draft.blocking.enforce).toEqual({
      dohAlias: 'doh_servers',
      exemptClients: 'grown_ups',
      exemptDestinations: 'work_cloud',
    })
  })

  it('names the DNS blocking setting that uses an alias', () => {
    const config = useConfigStore()
    config.replaceDraft(enforced())
    expect(config.aliasReferences('doh')).toEqual(['DNS blocking: DoH servers'])
    expect(config.aliasReferences('kids')).toEqual(['DNS blocking: exempt clients'])
    expect(config.aliasReferences('cloud')).toEqual(['DNS blocking: exempt destinations'])
  })

  it('names both exceptions when one alias is in each', () => {
    const config = useConfigStore()
    const d = enforced()
    d.blocking.enforce.exemptDestinations = 'kids'
    config.replaceDraft(d)
    expect(config.aliasReferences('kids')).toEqual([
      'DNS blocking: exempt clients',
      'DNS blocking: exempt destinations',
    ])
    config.upsertAlias({ name: 'grown_ups', type: 'hosts', entries: ['192.168.1.20'] }, 'kids')
    expect(config.draft.blocking.enforce).toEqual({
      dohAlias: 'doh',
      exemptClients: 'grown_ups',
      exemptDestinations: 'grown_ups',
    })
  })
})

describe('config store gateways', () => {
  beforeEach(() => setActivePinia(createPinia()))

  /** A gateway and a group rules and this router's lookups go through. */
  function routed() {
    return {
      ...draft(),
      gateways: [
        { name: 'vpn', enabled: true, interface: 'wg1' },
        { name: 'wan', enabled: true, interface: 'eth0' },
      ],
      gatewayGroups: [
        {
          name: 'private',
          enabled: true,
          members: [{ gateway: 'vpn' }, { gateway: 'wan', tier: 1 }],
        },
      ],
      rules: [
        { id: 'tv', zone: 'lan', gateway: 'vpn' },
        { id: 'guests', zone: 'lan', gateway: 'private' },
      ],
      services: { dns: { enabled: true, via: 'vpn' } },
    }
  }

  it('takes what routes through a gateway to its new name', () => {
    const config = useConfigStore()
    config.replaceDraft(routed())
    config.upsertGateway({ name: 'provider', enabled: true, interface: 'wg1' }, 'vpn')
    expect(config.rules.find((r) => r.id === 'tv').gateway).toBe('provider')
    expect(config.gatewayGroups[0].members[0].gateway).toBe('provider')
    expect(config.draft.services.dns.via).toBe('provider')

    config.upsertGatewayGroup({ ...config.gatewayGroups[0], name: 'hidden' }, 'private')
    expect(config.rules.find((r) => r.id === 'guests').gateway).toBe('hidden')
  })

  it("sends this router's lookups back to the default route with their gateway", () => {
    const config = useConfigStore()
    config.replaceDraft(routed())
    expect(config.gatewayDependents('vpn')).toContain(
      "this router's lookups go back to the default route",
    )
    config.removeGateway('vpn')
    expect('via' in config.draft.services.dns).toBe(false)
    expect(config.rules.find((r) => r.id === 'tv').gateway).toBeUndefined()
  })
})

describe('config store dirty', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('ignores the order a dialog rebuilt the keys in, not the order of a list', () => {
    const config = useConfigStore()
    config.saved = {
      version: 3,
      zones: [{ name: 'lan' }, { name: 'wan', external: true }],
      interfaces: [{ name: 'eth0', zone: 'wan', enabled: true, mtu: 9000 }],
    }
    config.replaceDraft({
      version: 3,
      zones: [{ name: 'lan' }, { name: 'wan', external: true }],
      interfaces: [{ name: 'eth0', enabled: true, mtu: 9000, zone: 'wan' }],
    })
    expect(config.dirty).toBe(false)
    config.draft.zones.reverse()
    expect(config.dirty).toBe(true)
    config.draft.zones.reverse()
    config.draft.interfaces[0].mtu = 1500
    expect(config.dirty).toBe(true)
  })
})

describe('config store put back', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function seeded(extra = {}) {
    const config = useConfigStore()
    const d = { ...draft(), ...extra }
    config.saved = JSON.parse(JSON.stringify(d))
    config.replaceDraft(d)
    return config
  }

  it('drops the wireless country, and the block, when it is set back to nothing', () => {
    const config = seeded()
    config.setWirelessCountry('US')
    expect(config.dirty).toBe(true)
    config.setWirelessCountry('')
    expect(config.dirty).toBe(false)
  })

  it('drops a defence value set to nothing', () => {
    const config = seeded()
    config.setDefence('synFlood', { rate: 30, burst: 60 })
    config.setDefence('synFlood', { burst: null })
    expect(config.draft.protection.synFlood).toEqual({ rate: 30 })
  })

  it('drops remote backup fields cleared again, and the block with them', () => {
    const config = seeded()
    config.setRemoteBackup({ bucket: 'router-backups' })
    config.setRemoteBackup({ enabled: true })
    expect(config.draft.backup.remote).toEqual({ bucket: 'router-backups', enabled: true })
    config.setRemoteBackup({ enabled: false })
    expect(config.draft.backup.remote).toEqual({ bucket: 'router-backups', enabled: false })
    config.setRemoteBackup({ bucket: '' })
    expect(config.draft.backup).toBeUndefined()
    expect(config.dirty).toBe(false)
  })

  it('drops update schedules and exclusions cleared again', () => {
    const config = seeded({ updates: { system: {}, ostiole: {} } })
    config.setUpdates('system', { checkSchedule: '0 3 * * *', exclude: ['kernel*'] })
    expect(config.dirty).toBe(true)
    config.setUpdates('system', { checkSchedule: '', exclude: [] })
    expect(config.dirty).toBe(false)
  })
})
