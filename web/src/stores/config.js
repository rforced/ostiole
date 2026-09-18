import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

import { ApiError, api } from '@/lib/api'
import { useToastStore } from '@/stores/toast'

const clone = (v) => (v === null || v === undefined ? v : JSON.parse(JSON.stringify(v)))

/** How long the diff waits after the last edit before asking the server. */
const DIFF_DEBOUNCE_MS = 400
/** How long Undo stays on offer after a delete. */
const UNDO_MS = 8000

/**
 * Holds the saved configuration and an editable draft. Pages mutate the
 * draft; ApplyBar checks and applies it with a confirmation window.
 */
export const useConfigStore = defineStore('config', () => {
  const saved = ref(null)
  const draft = ref(null)
  const loaded = ref(false)
  const error = ref('')
  /**
   * Bumped every time an apply reaches the kernel. Pages that show live
   * state watch it and re-read, so a deleted VLAN leaves the interfaces
   * table without anyone pressing reload.
   */
  const applied = ref(0)

  const dirty = computed(() => JSON.stringify(saved.value) !== JSON.stringify(draft.value))
  const zones = computed(() => draft.value?.zones ?? [])
  const interfaces = computed(() => draft.value?.interfaces ?? [])
  const aliases = computed(() => draft.value?.aliases ?? [])
  const rules = computed(() => draft.value?.rules ?? [])

  async function load(force = false) {
    if (loaded.value && !force) return
    error.value = ''
    try {
      saved.value = await api.config.get()
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) saved.value = null
      else {
        error.value = e instanceof Error ? e.message : String(e)
        return
      }
    }
    draft.value = clone(saved.value)
    loaded.value = true
  }

  /**
   * Runs a mutation and reports it with a way back. Undo puts the draft
   * back exactly as it was before the mutation, so an edit made in the
   * few seconds the offer stands goes with it. That is simpler to trust
   * than a merge, and the toast says what it restores.
   */
  function undoable(message, mutate) {
    const before = clone(draft.value)
    mutate()
    useToastStore().show(message, {
      timeout: UNDO_MS,
      action: { label: 'Undo', run: () => (draft.value = before) },
    })
  }

  function discard() {
    if (!dirty.value) return
    undoable('Draft discarded.', () => (draft.value = clone(saved.value)))
  }

  /** After a confirmed apply the draft becomes the saved state. */
  function markSaved() {
    saved.value = clone(draft.value)
  }

  /** Called whenever the kernel has just been changed, applied or rolled back. */
  function markApplied() {
    applied.value++
  }

  /** Replace the draft wholesale, e.g. with an archived revision. */
  function replaceDraft(cfg) {
    draft.value = clone(cfg)
  }

  function reset() {
    saved.value = null
    draft.value = null
    loaded.value = false
  }

  // ---- interfaces ----------------------------------------------------

  function findInterface(name) {
    return interfaces.value.find((i) => i.name === name) ?? null
  }

  function upsertInterface(iface) {
    const list = draft.value.interfaces ?? (draft.value.interfaces = [])
    const idx = list.findIndex((i) => i.name === iface.name)
    if (idx === -1) list.push(clone(iface))
    else list[idx] = clone(iface)
  }

  /**
   * What goes with an interface when it is removed, in words, for the
   * dialog. Anything that names it means nothing without it: a DHCP
   * server, a gateway, a route, a DNS listener. A VLAN or PPPoE session on
   * top of it cannot exist without it. A bridge or bond carries on one
   * member short unless that was its only one, and an interface delegated
   * a prefix from it falls back to no IPv6.
   */
  function interfaceDependents(name) {
    const out = []
    const d = draft.value
    if (!d) return out
    for (const i of interfaces.value) {
      if (i.vlan?.parent === name || i.pppoe?.parent === name) {
        out.push(`${i.vlan ? 'VLAN' : 'PPPoE'} ${i.name}`, ...interfaceDependents(i.name))
      }
      const agg = i.bridge ?? i.bond
      if (agg?.members?.includes(name)) {
        if (agg.members.length === 1) {
          out.push(`${i.bridge ? 'bridge' : 'bond'} ${i.name}, its only member`)
          out.push(...interfaceDependents(i.name))
        } else out.push(`${i.name} loses member ${name}`)
      }
      if (i.ipv6?.delegatedFrom === name) out.push(`${i.name} loses its delegated IPv6 prefix`)
    }
    const dhcp = d.services?.dhcp ?? {}
    if ((dhcp.servers ?? []).some((s) => s.interface === name)) out.push(`DHCP server on ${name}`)
    if ((dhcp.v6 ?? []).some((s) => s.interface === name)) {
      out.push(`IPv6 advertisement on ${name}`)
    }
    if ((d.services?.dns?.interfaces ?? []).includes(name)) out.push(`DNS listener on ${name}`)
    for (const g of gateways.value) {
      if (g.interface === name) out.push(`gateway ${g.name}`, ...gatewayDependents(g.name))
    }
    for (const r of routes.value) if (r.interface === name) out.push(`route ${r.id}`)
    return out
  }

  /** The mutation behind removeInterface, in the order interfaceDependents lists it. */
  function dropInterface(name) {
    const d = draft.value
    for (const i of [...interfaces.value]) {
      if (i.vlan?.parent === name || i.pppoe?.parent === name) dropInterface(i.name)
      const agg = i.bridge ?? i.bond
      if (agg?.members?.includes(name)) {
        if (agg.members.length === 1) dropInterface(i.name)
        else {
          agg.members = agg.members.filter((m) => m !== name)
          if (agg.primary === name) delete agg.primary
        }
      }
      if (i.ipv6?.delegatedFrom === name) i.ipv6 = { mode: 'none' }
    }
    const dhcp = d.services?.dhcp
    if (dhcp?.servers) dhcp.servers = dhcp.servers.filter((s) => s.interface !== name)
    if (dhcp?.v6) dhcp.v6 = dhcp.v6.filter((s) => s.interface !== name)
    const dns = d.services?.dns
    if (dns?.interfaces) dns.interfaces = dns.interfaces.filter((n) => n !== name)
    for (const g of [...gateways.value]) if (g.interface === name) dropGateway(g.name)
    if (d.routes) d.routes = d.routes.filter((r) => r.interface !== name)
    d.interfaces = interfaces.value.filter((i) => i.name !== name)
  }

  function removeInterface(name) {
    undoable(`Removed ${name} from the configuration.`, () => dropInterface(name))
  }

  // ---- WireGuard -------------------------------------------------------

  /** Tunnels are interfaces with a wireguard block. */
  const tunnels = computed(() => interfaces.value.filter((i) => i.wireguard))

  /** The tailnet node, if this router has one. There is at most one. */
  const tailscale = computed(() => interfaces.value.find((i) => i.tailscale) ?? null)

  function removeTunnel(name) {
    undoable(`Deleted tunnel ${name}.`, () => dropInterface(name))
  }

  function upsertPeer(tunnelName, peer, previousName = peer.name) {
    const t = findInterface(tunnelName)
    if (!t?.wireguard) return
    const list = t.wireguard.peers ?? (t.wireguard.peers = [])
    const idx = list.findIndex((p) => p.name === previousName)
    if (idx === -1) list.push(clone(peer))
    else list[idx] = clone(peer)
  }

  function removePeer(tunnelName, peerName) {
    const t = findInterface(tunnelName)
    if (!t?.wireguard) return
    undoable(`Deleted peer ${peerName}.`, () => {
      t.wireguard.peers = (t.wireguard.peers ?? []).filter((p) => p.name !== peerName)
    })
  }

  // ---- zones -----------------------------------------------------------

  function upsertZone(zone, previousName = zone.name) {
    const list = draft.value.zones ?? (draft.value.zones = [])
    const idx = list.findIndex((z) => z.name === previousName)
    if (idx === -1) list.push(clone(zone))
    else list[idx] = clone(zone)
    if (previousName !== zone.name) renameZoneReferences(previousName, zone.name)
  }

  function renameZoneReferences(from, to) {
    for (const i of interfaces.value) if (i.zone === from) i.zone = to
    for (const r of rules.value) {
      if (r.zone === from) r.zone = to
      if (r.destZone === from) r.destZone = to
    }
    for (const pf of draft.value.nat?.portForwards ?? []) if (pf.zone === from) pf.zone = to
    for (const o of draft.value.nat?.outbound?.rules ?? []) if (o.zone === from) o.zone = to
    for (const o of draft.value.nat?.oneToOne ?? []) if (o.zone === from) o.zone = to
  }

  /**
   * Interfaces assigned to a zone. They are the one thing that stops a
   * zone being deleted: an interface has to be somewhere, and moving it
   * is a decision only the admin can make.
   */
  function zoneInterfaces(name) {
    return interfaces.value.filter((i) => i.zone === name).map((i) => i.name)
  }

  /**
   * Rules and NAT entries written against a zone. They go with it when it
   * is deleted: none of them mean anything without their zone, and a rule
   * left pointing at a zone that is gone fails validation.
   */
  function zoneDependents(name) {
    const refs = []
    for (const r of rules.value)
      if (r.zone === name || r.destZone === name) refs.push(`rule ${r.id}`)
    for (const pf of draft.value.nat?.portForwards ?? [])
      if (pf.zone === name) refs.push(`port forward ${pf.id}`)
    for (const o of draft.value.nat?.outbound?.rules ?? [])
      if (o.zone === name) refs.push(`outbound NAT ${o.id}`)
    for (const o of draft.value.nat?.oneToOne ?? [])
      if (o.zone === name) refs.push(`1:1 NAT ${o.id}`)
    return refs
  }

  /**
   * Deletes a zone and everything written against it. A rule that only
   * named the zone as its destination goes too rather than losing the
   * restriction: widening an allow rule to every destination is not
   * something a delete should do quietly.
   */
  function removeZone(name) {
    undoable(`Deleted zone ${name}.`, () => {
      draft.value.zones = zones.value.filter((z) => z.name !== name)
      draft.value.rules = rules.value.filter((r) => r.zone !== name && r.destZone !== name)
      const n = draft.value.nat
      if (!n) return
      if (n.portForwards) n.portForwards = n.portForwards.filter((pf) => pf.zone !== name)
      if (n.outbound?.rules) n.outbound.rules = n.outbound.rules.filter((o) => o.zone !== name)
      if (n.oneToOne) n.oneToOne = n.oneToOne.filter((o) => o.zone !== name)
    })
  }

  // ---- rules -----------------------------------------------------------

  function rulesForZone(zone) {
    return rules.value.filter((r) => r.zone === zone)
  }

  function upsertRule(rule) {
    const list = draft.value.rules ?? (draft.value.rules = [])
    const idx = list.findIndex((r) => r.id === rule.id)
    if (idx === -1) list.push(clone(rule))
    else list[idx] = clone(rule)
  }

  function removeRule(id) {
    undoable(`Deleted rule ${id}.`, () => {
      draft.value.rules = rules.value.filter((r) => r.id !== id)
    })
  }

  /** Moves a rule up (-1) or down (+1) among the rules of its zone. */
  function moveRule(id, delta) {
    const list = draft.value.rules
    const from = list.findIndex((r) => r.id === id)
    if (from === -1) return
    const zone = list[from].zone
    let to = from + delta
    while (to >= 0 && to < list.length && list[to].zone !== zone) to += delta
    if (to < 0 || to >= list.length) return
    ;[list[from], list[to]] = [list[to], list[from]]
  }

  // ---- aliases ---------------------------------------------------------

  function upsertAlias(alias, previousName = alias.name) {
    const list = draft.value.aliases ?? (draft.value.aliases = [])
    const idx = list.findIndex((a) => a.name === previousName)
    if (idx === -1) list.push(clone(alias))
    else list[idx] = clone(alias)
    if (previousName !== alias.name) {
      for (const r of rules.value) {
        for (const side of [r.source, r.destination]) {
          if (side?.alias === previousName) side.alias = alias.name
          if (side?.portAlias === previousName) side.portAlias = alias.name
        }
      }
    }
  }

  function aliasReferences(name) {
    const refs = []
    for (const r of rules.value) {
      if (r.source?.alias === name || r.source?.portAlias === name) refs.push(`rule ${r.id} source`)
      if (r.destination?.alias === name || r.destination?.portAlias === name)
        refs.push(`rule ${r.id} destination`)
    }
    const enforce = draft.value?.blocking?.enforce ?? {}
    if (enforce.dohAlias === name) refs.push('DNS blocking: DoH servers')
    if (enforce.exemptAlias === name) refs.push('DNS blocking: exempt clients')
    return refs
  }

  function removeAlias(name) {
    undoable(`Deleted alias ${name}.`, () => {
      draft.value.aliases = aliases.value.filter((a) => a.name !== name)
    })
  }

  // ---- NAT -------------------------------------------------------------

  const nat = computed(() => draft.value?.nat ?? { outbound: { mode: 'automatic' } })

  function ensureNat() {
    if (!draft.value.nat) draft.value.nat = { outbound: { mode: 'automatic' } }
    if (!draft.value.nat.outbound) draft.value.nat.outbound = { mode: 'automatic' }
    return draft.value.nat
  }

  function setOutboundMode(mode) {
    ensureNat().outbound.mode = mode
  }

  function upsertPortForward(pf) {
    const n = ensureNat()
    const list = n.portForwards ?? (n.portForwards = [])
    const idx = list.findIndex((x) => x.id === pf.id)
    if (idx === -1) list.push(clone(pf))
    else list[idx] = clone(pf)
  }

  function removePortForward(id) {
    undoable(`Deleted port forward ${id}.`, () => {
      const n = ensureNat()
      n.portForwards = (n.portForwards ?? []).filter((x) => x.id !== id)
    })
  }

  function upsertOneToOne(entry) {
    const n = ensureNat()
    const list = n.oneToOne ?? (n.oneToOne = [])
    const idx = list.findIndex((x) => x.id === entry.id)
    if (idx === -1) list.push(clone(entry))
    else list[idx] = clone(entry)
  }

  function removeOneToOne(id) {
    undoable(`Deleted 1:1 mapping ${id}.`, () => {
      const n = ensureNat()
      n.oneToOne = (n.oneToOne ?? []).filter((x) => x.id !== id)
    })
  }

  function upsertOutboundRule(rule) {
    const n = ensureNat()
    const list = n.outbound.rules ?? (n.outbound.rules = [])
    const idx = list.findIndex((x) => x.id === rule.id)
    if (idx === -1) list.push(clone(rule))
    else list[idx] = clone(rule)
  }

  function removeOutboundRule(id) {
    undoable(`Deleted outbound rule ${id}.`, () => {
      const n = ensureNat()
      n.outbound.rules = (n.outbound.rules ?? []).filter((x) => x.id !== id)
    })
  }

  // ---- traffic shaping -------------------------------------------------

  /** Interfaces that have been given a line speed, in configuration order. */
  const shapedInterfaces = computed(() =>
    interfaces.value.filter((i) => i.shaping && (i.shaping.download || i.shaping.upload)),
  )

  function setShaping(name, shaping) {
    const iface = findInterface(name)
    if (!iface) return
    iface.shaping = clone(shaping)
  }

  function clearShaping(name) {
    const iface = findInterface(name)
    if (!iface?.shaping) return
    undoable(`Removed the speed set on ${name}.`, () => {
      delete findInterface(name).shaping
    })
  }

  /** Zones that hold back a host with a lot of connections open. */
  const busyZones = computed(() => zones.value.filter((z) => z.busy))

  function setBusy(zone, busy) {
    const z = zones.value.find((x) => x.name === zone)
    if (!z) return
    z.busy = clone(busy)
  }

  function clearBusy(zone) {
    const z = zones.value.find((x) => x.name === zone)
    if (!z?.busy) return
    undoable(`Stopped holding back busy hosts on ${zone}.`, () => {
      delete zones.value.find((x) => x.name === zone).busy
    })
  }

  /** The rules and port forwards that put their traffic in a tier. */
  const shapedRules = computed(() => rules.value.filter((r) => r.priority))
  const shapedForwards = computed(() => (nat.value.portForwards ?? []).filter((pf) => pf.priority))

  // ---- schedules -------------------------------------------------------

  const schedules = computed(() => draft.value?.schedules ?? [])

  function upsertSchedule(schedule, previousName = schedule.name) {
    const list = draft.value.schedules ?? (draft.value.schedules = [])
    const idx = list.findIndex((s) => s.name === previousName)
    if (idx === -1) list.push(clone(schedule))
    else list[idx] = clone(schedule)
    if (previousName !== schedule.name) {
      for (const r of rules.value) if (r.schedule === previousName) r.schedule = schedule.name
    }
  }

  function scheduleReferences(name) {
    return rules.value.filter((r) => r.schedule === name).map((r) => `rule ${r.id}`)
  }

  function removeSchedule(name) {
    undoable(`Deleted schedule ${name}.`, () => {
      draft.value.schedules = schedules.value.filter((s) => s.name !== name)
    })
  }

  // ---- services --------------------------------------------------------

  /** Returns the services block, creating defaults in the draft as needed. */
  function ensureServices() {
    const d = draft.value
    if (!d.services) d.services = { dhcp: { enabled: false }, dns: { enabled: false } }
    if (!d.services.dhcp) d.services.dhcp = { enabled: false }
    if (!d.services.dns) d.services.dns = { enabled: false }
    return d.services
  }

  function upsertServer(server) {
    const dhcp = ensureServices().dhcp
    const list = dhcp.servers ?? (dhcp.servers = [])
    const idx = list.findIndex((s) => s.interface === server.interface)
    if (idx === -1) list.push(clone(server))
    else list[idx] = clone(server)
  }

  function removeServer(iface) {
    undoable(`Deleted the DHCP server on ${iface}.`, () => {
      const dhcp = ensureServices().dhcp
      dhcp.servers = (dhcp.servers ?? []).filter((s) => s.interface !== iface)
    })
  }

  function upsertV6Server(server) {
    const dhcp = ensureServices().dhcp
    const list = dhcp.v6 ?? (dhcp.v6 = [])
    const idx = list.findIndex((s) => s.interface === server.interface)
    if (idx === -1) list.push(clone(server))
    else list[idx] = clone(server)
  }

  function removeV6Server(iface) {
    undoable(`Stopped advertising IPv6 on ${iface}.`, () => {
      const dhcp = ensureServices().dhcp
      dhcp.v6 = (dhcp.v6 ?? []).filter((s) => s.interface !== iface)
    })
  }

  function upsertStaticLease(lease, previousMac = lease.mac) {
    const dhcp = ensureServices().dhcp
    const list = dhcp.staticLeases ?? (dhcp.staticLeases = [])
    const idx = list.findIndex((l) => l.mac.toLowerCase() === previousMac.toLowerCase())
    if (idx === -1) list.push(clone(lease))
    else list[idx] = clone(lease)
  }

  function removeStaticLease(mac) {
    undoable(`Deleted the static lease for ${mac}.`, () => {
      const dhcp = ensureServices().dhcp
      dhcp.staticLeases = (dhcp.staticLeases ?? []).filter(
        (l) => l.mac.toLowerCase() !== mac.toLowerCase(),
      )
    })
  }

  function upsertHostOverride(host, previousName = host.hostname) {
    const dns = ensureServices().dns
    const list = dns.hostOverrides ?? (dns.hostOverrides = [])
    const idx = list.findIndex((h) => h.hostname.toLowerCase() === previousName.toLowerCase())
    if (idx === -1) list.push(clone(host))
    else list[idx] = clone(host)
  }

  function removeHostOverride(name) {
    undoable(`Deleted host override ${name}.`, () => {
      const dns = ensureServices().dns
      dns.hostOverrides = (dns.hostOverrides ?? []).filter(
        (h) => h.hostname.toLowerCase() !== name.toLowerCase(),
      )
    })
  }

  function upsertDomainOverride(override, previousDomain = override.domain) {
    const dns = ensureServices().dns
    const list = dns.domainOverrides ?? (dns.domainOverrides = [])
    const idx = list.findIndex((d) => d.domain.toLowerCase() === previousDomain.toLowerCase())
    if (idx === -1) list.push(clone(override))
    else list[idx] = clone(override)
  }

  function removeDomainOverride(domain) {
    undoable(`Deleted domain override ${domain}.`, () => {
      const dns = ensureServices().dns
      dns.domainOverrides = (dns.domainOverrides ?? []).filter(
        (d) => d.domain.toLowerCase() !== domain.toLowerCase(),
      )
    })
  }

  /** The port mapping block, created in the draft on first use. */
  function ensureUPnP() {
    const services = ensureServices()
    if (!services.upnp) services.upnp = { enabled: false }
    return services.upnp
  }

  /**
   * Access list entries are read in order and have no name, so they are
   * addressed by position. A negative index appends.
   */
  function upsertUPnPRule(rule, index = -1) {
    const upnp = ensureUPnP()
    const list = upnp.acl ?? (upnp.acl = [])
    if (index < 0 || index >= list.length) list.push(clone(rule))
    else list[index] = clone(rule)
  }

  function removeUPnPRule(index) {
    undoable('Deleted the access list entry.', () => {
      const upnp = ensureUPnP()
      upnp.acl = (upnp.acl ?? []).filter((_, i) => i !== index)
    })
  }

  function moveUPnPRule(index, delta) {
    const list = ensureUPnP().acl ?? []
    const to = index + delta
    if (to < 0 || to >= list.length) return
    ;[list[index], list[to]] = [list[to], list[index]]
  }

  // ---- gateways --------------------------------------------------------

  const gateways = computed(() => draft.value?.gateways ?? [])

  function upsertGateway(gateway, previousName = gateway.name) {
    const list = draft.value.gateways ?? (draft.value.gateways = [])
    const idx = list.findIndex((g) => g.name === previousName)
    if (idx === -1) list.push(clone(gateway))
    else list[idx] = clone(gateway)
  }

  const gatewayGroups = computed(() => draft.value?.gatewayGroups ?? [])

  /** Rules routed through a group, which lose that when it goes. */
  function groupDependents(name) {
    return rules.value
      .filter((r) => r.gateway === name)
      .map((r) => `rule ${r.id} loses its gateway`)
  }

  /**
   * What changes when a gateway goes: rules routed through it fall back
   * to the default route, groups carry on without it, and a group it was
   * the only member of goes too.
   */
  function gatewayDependents(name) {
    const out = rules.value
      .filter((r) => r.gateway === name)
      .map((r) => `rule ${r.id} loses its gateway`)
    for (const g of gatewayGroups.value) {
      const members = g.members ?? []
      if (!members.some((m) => m.gateway === name)) continue
      if (members.length === 1)
        out.push(`group ${g.name}, its only member`, ...groupDependents(g.name))
      else out.push(`group ${g.name} loses member ${name}`)
    }
    return out
  }

  function dropGatewayGroup(name) {
    for (const r of rules.value) if (r.gateway === name) delete r.gateway
    draft.value.gatewayGroups = gatewayGroups.value.filter((g) => g.name !== name)
  }

  function dropGateway(name) {
    for (const r of rules.value) if (r.gateway === name) delete r.gateway
    for (const g of [...gatewayGroups.value]) {
      const members = g.members ?? []
      if (!members.some((m) => m.gateway === name)) continue
      if (members.length === 1) dropGatewayGroup(g.name)
      else g.members = members.filter((m) => m.gateway !== name)
    }
    draft.value.gateways = gateways.value.filter((g) => g.name !== name)
  }

  function removeGateway(name) {
    undoable(`Deleted gateway ${name}.`, () => dropGateway(name))
  }

  /** Everything rules can route through: gateways first, then groups. */
  const routeTargets = computed(() => [
    ...gateways.value.map((g) => ({ name: g.name, kind: 'gateway', enabled: g.enabled })),
    ...gatewayGroups.value.map((g) => ({ name: g.name, kind: 'group', enabled: g.enabled })),
  ])

  function upsertGatewayGroup(group, previousName = group.name) {
    const list = draft.value.gatewayGroups ?? (draft.value.gatewayGroups = [])
    const idx = list.findIndex((g) => g.name === previousName)
    if (idx === -1) list.push(clone(group))
    else list[idx] = clone(group)
  }

  function removeGatewayGroup(name) {
    undoable(`Deleted gateway group ${name}.`, () => dropGatewayGroup(name))
  }

  /** Where a gateway or group is used, so deleting it cannot go unnoticed. */
  function gatewayReferences(name) {
    const refs = rules.value.filter((r) => r.gateway === name).map((r) => `rule ${r.id}`)
    for (const g of gatewayGroups.value) {
      if ((g.members ?? []).some((m) => m.gateway === name)) refs.push(`group ${g.name}`)
    }
    return refs
  }

  // ---- updates ---------------------------------------------------------

  /**
   * The update settings, created on first use. An empty block means the
   * defaults, which the daemon fills in: security fixes, weekly.
   */
  function ensureUpdates() {
    const d = draft.value
    if (!d.updates) d.updates = {}
    if (!d.updates.system) d.updates.system = {}
    if (!d.updates.ostiole) d.updates.ostiole = {}
    return d.updates
  }

  const updates = computed(() => draft.value?.updates ?? {})
  const systemUpdates = computed(() => updates.value.system ?? {})
  const ostioleUpdates = computed(() => updates.value.ostiole ?? {})

  /**
   * @param {"system"|"ostiole"} which
   * @param {object} patch fields to change
   */
  function setUpdates(which, patch) {
    Object.assign(ensureUpdates()[which], patch)
  }

  // ---- protection ------------------------------------------------------

  /** The edge defence, or an empty one on a draft that has never had it. */
  const protection = computed(() => draft.value?.protection ?? {})

  /**
   * Turn one defence on or off. A limit is an object or nothing at all:
   * the renderer reads "is it there" rather than an enabled flag, so
   * switching one off removes it.
   *
   * @param {"synFlood"|"icmpFlood"|"portScan"} which
   * @param {object|null} value the limit, or null to switch it off
   */
  function setDefence(which, value) {
    const p = draft.value.protection ?? (draft.value.protection = {})
    if (value === null) {
      delete p[which]
      return
    }
    p[which] = { ...(p[which] ?? {}), ...clone(value) }
  }

  /** The zones defended; an empty list means every external zone. */
  function setProtectedZones(names) {
    const p = draft.value.protection ?? (draft.value.protection = {})
    if (!names.length) {
      delete p.zones
      return
    }
    p.zones = [...names]
  }

  // ---- crons -----------------------------------------------------------

  const crons = computed(() => draft.value?.crons ?? [])

  function upsertCron(cron) {
    const list = draft.value.crons ?? (draft.value.crons = [])
    const idx = list.findIndex((c) => c.id === cron.id)
    if (idx === -1) list.push(clone(cron))
    else list[idx] = clone(cron)
  }

  function removeCron(id) {
    undoable(`Deleted cron ${id}.`, () => {
      draft.value.crons = crons.value.filter((c) => c.id !== id)
    })
  }

  // ---- routes ----------------------------------------------------------

  const routes = computed(() => draft.value?.routes ?? [])

  function upsertRoute(route) {
    const list = draft.value.routes ?? (draft.value.routes = [])
    const idx = list.findIndex((r) => r.id === route.id)
    if (idx === -1) list.push(clone(route))
    else list[idx] = clone(route)
  }

  function removeRoute(id) {
    undoable(`Deleted route ${id}.`, () => {
      draft.value.routes = routes.value.filter((r) => r.id !== id)
    })
  }

  // ---- DNS blocking ----------------------------------------------------

  const blocking = computed(() => draft.value?.blocking ?? { enabled: false })

  function ensureBlocking() {
    const d = draft.value
    if (!d.blocking) d.blocking = { enabled: false }
    if (!d.blocking.enforce) d.blocking.enforce = {}
    return d.blocking
  }

  const blockLists = computed(() => draft.value?.blocking?.lists ?? [])

  function upsertBlockList(list, previousName = list.name) {
    const b = ensureBlocking()
    const all = b.lists ?? (b.lists = [])
    const idx = all.findIndex((l) => l.name === previousName)
    if (idx === -1) all.push(clone(list))
    else all[idx] = clone(list)
  }

  function removeBlockList(name) {
    undoable(`Deleted block list ${name}.`, () => {
      const b = ensureBlocking()
      b.lists = (b.lists ?? []).filter((l) => l.name !== name)
    })
  }

  // ---- what the draft changes ------------------------------------------

  /**
   * The draft compared with the saved configuration, as the server sees
   * it. Refreshed a moment after the last edit. It is a nicety: the apply
   * bar works without it, so a failed diff is not an error anyone sees.
   *
   * @type {import('vue').Ref<{path: string, kind: string, before?: unknown, after?: unknown}[]>}
   */
  const changes = ref([])
  let diffTimer = 0
  let diffSeq = 0

  async function refreshChanges() {
    if (!dirty.value || !saved.value) {
      changes.value = []
      return
    }
    const seq = ++diffSeq
    try {
      const out = await api.config.diff({ from: 'current', toConfig: draft.value })
      if (seq === diffSeq) changes.value = out
    } catch {
      /* see above */
    }
  }

  watch(
    [draft, saved],
    () => {
      window.clearTimeout(diffTimer)
      if (!dirty.value) {
        changes.value = []
        return
      }
      diffTimer = window.setTimeout(refreshChanges, DIFF_DEBOUNCE_MS)
    },
    { deep: true },
  )

  /**
   * The sidebar path a change belongs under. Diff paths name the entry
   * (`rules[web-in].action`), so a tunnel is told from an interface by
   * looking it up.
   */
  function sectionFor(path) {
    const [, key, id] = /^([A-Za-z0-9]+)(?:\[([^\]]*)\])?/.exec(path) ?? []
    switch (key) {
      case 'interfaces': {
        // A speed lives on the interface but is edited on its own page, so
        // the change is shown where it was made.
        if (path.includes('.shaping')) return '/firewall/shaping'
        const i =
          interfaces.value.find((x) => x.name === id) ??
          saved.value?.interfaces?.find((x) => x.name === id)
        if (i?.wireguard) return '/vpn/wireguard'
        if (i?.tailscale) return '/vpn/tailscale'
        return '/interfaces'
      }
      case 'zones':
        // Holding back a busy host is a priority decision, so it is edited
        // and shown where the other priorities are.
        return path.includes('.busy') ? '/firewall/shaping' : '/interfaces'
      case 'protection':
        return '/firewall/protection'
      case 'rules':
        return '/firewall/rules'
      case 'aliases':
        return '/firewall/aliases'
      case 'schedules':
        return '/firewall/schedules'
      case 'nat':
        return '/firewall/nat'
      case 'gateways':
      case 'gatewayGroups':
      case 'routes':
        return '/routing'
      case 'services':
        return path.startsWith('services.dns') ? '/services/dns' : '/services/dhcp'
      case 'blocking':
        return '/services/dns'
      case 'crons':
        return '/crons'
      case 'updates':
        return '/system/updates'
      case 'system':
        return path.startsWith('system.keepRevisions') ? '/system/backup' : '/system/general'
      default:
        return '/system/general'
    }
  }

  const changedPaths = computed(() => new Set(changes.value.map((c) => sectionFor(c.path))))

  /** Whether a sidebar item has anything unapplied under it. */
  function hasChanges(to) {
    for (const p of changedPaths.value) if (p === to || p.startsWith(`${to}/`)) return true
    return false
  }

  /**
   * Whether the draft touches one entry of a named list, for marking its
   * row: `isChanged('rules', 'web-in')`, `isChanged('nat.portForwards', id)`.
   */
  function isChanged(list, key) {
    const entry = `${list}[${key}]`
    return changes.value.some(
      (c) => c.path === entry || c.path.startsWith(`${entry}.`) || c.path.startsWith(`${entry}[`),
    )
  }

  return {
    changes,
    refreshChanges,
    hasChanges,
    isChanged,
    sectionFor,
    interfaceDependents,
    removeTunnel,
    gatewayDependents,
    groupDependents,
    saved,
    draft,
    loaded,
    error,
    dirty,
    applied,
    zones,
    interfaces,
    aliases,
    rules,
    protection,
    setDefence,
    setProtectedZones,
    load,
    discard,
    undoable,
    markSaved,
    markApplied,
    replaceDraft,
    reset,
    findInterface,
    upsertInterface,
    removeInterface,
    tunnels,
    tailscale,
    upsertPeer,
    removePeer,
    upsertZone,
    zoneInterfaces,
    zoneDependents,
    removeZone,
    rulesForZone,
    upsertRule,
    removeRule,
    moveRule,
    upsertAlias,
    aliasReferences,
    removeAlias,
    nat,
    setOutboundMode,
    upsertPortForward,
    removePortForward,
    upsertOneToOne,
    removeOneToOne,
    shapedInterfaces,
    setShaping,
    clearShaping,
    shapedRules,
    shapedForwards,
    busyZones,
    setBusy,
    clearBusy,
    schedules,
    upsertSchedule,
    scheduleReferences,
    removeSchedule,
    blocking,
    ensureBlocking,
    blockLists,
    upsertBlockList,
    removeBlockList,
    upsertOutboundRule,
    removeOutboundRule,
    routes,
    crons,
    upsertCron,
    removeCron,
    updates,
    systemUpdates,
    ostioleUpdates,
    setUpdates,
    gateways,
    upsertGateway,
    removeGateway,
    gatewayGroups,
    routeTargets,
    upsertGatewayGroup,
    removeGatewayGroup,
    gatewayReferences,
    upsertRoute,
    removeRoute,
    ensureServices,
    upsertServer,
    removeServer,
    upsertV6Server,
    removeV6Server,
    upsertStaticLease,
    removeStaticLease,
    upsertHostOverride,
    removeHostOverride,
    upsertDomainOverride,
    removeDomainOverride,
    ensureUPnP,
    upsertUPnPRule,
    removeUPnPRule,
    moveUPnPRule,
  }
})
