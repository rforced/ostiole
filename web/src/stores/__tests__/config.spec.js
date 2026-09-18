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
    expect(config.hasChanges('/system/backup')).toBe(true)
    expect(config.hasChanges('/')).toBe(false)

    expect(config.isChanged('rules', 'r1')).toBe(true)
    expect(config.isChanged('rules', 'r2')).toBe(false)
    expect(config.isChanged('services.dhcp.servers', 'lan')).toBe(true)
    expect(config.isChanged('interfaces[wg0].wireguard.peers', 'bob')).toBe(true)
    expect(config.isChanged('interfaces', 'wg0')).toBe(true)
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
})
