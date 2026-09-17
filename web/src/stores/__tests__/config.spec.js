import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'

/** A draft with a zone that everything else hangs off. */
function draft() {
  return {
    version: 2,
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
