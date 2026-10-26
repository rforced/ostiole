import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

import { ApiError, api } from '@/lib/api'
import type {
  ACMEAccount,
  Alias,
  BlockList,
  Blocking,
  BusyHosts,
  Certificate,
  Config,
  Cron,
  DDNSRecord,
  DHCPServer,
  DHCPService,
  DHCPv6Server,
  DNSProvider,
  DomainOverride,
  Endpoint,
  Gateway,
  GatewayGroup,
  HostOverride,
  Interface,
  Management,
  NAT,
  NTP,
  Notifications,
  OneToOneNAT,
  OutboundMode,
  OutboundRule,
  PackageUpdates,
  PortForward,
  Protection,
  Proxy,
  ProxyAccess,
  ProxyPool,
  ProxySite,
  Radio,
  RemoteBackup,
  Rule,
  Schedule,
  SelfUpdates,
  Services,
  Shaping,
  StaticLease,
  StaticRoute,
  UPnPRule,
  Updates,
  WAFExclusion,
  WAFProfile,
  WireGuardPeer,
  WoLDevice,
  Zone,
} from '@/lib/model.gen'
import { normalizeName } from '@/lib/blocking'
import { DISCOVERY_DEFAULT } from '@/lib/discovery'
import { exclusionText, sameExclusion } from '@/lib/exclusions'
import { overrideKey } from '@/lib/hosts'
import { certificateProvider, providerFor } from '@/lib/providers'
import { deviceName } from '@/lib/wol'
import { useToastStore } from '@/stores/toast'

const clone = <T>(v: T): T => (v === null || v === undefined ? v : JSON.parse(JSON.stringify(v)))

/** One difference between the draft and the saved configuration, as the server reports it. */
export interface ConfigChange {
  path: string
  kind: string
  before?: unknown
  after?: unknown
}

/** Swaps the item with the next one of its zone, delta -1 up or +1 down. */
function moveInZone<T extends { id: string; zone?: string }>(list: T[], id: string, delta: number) {
  const from = list.findIndex((r) => r.id === id)
  if (from === -1) return
  const zone = list[from].zone
  let to = from + delta
  while (to >= 0 && to < list.length && list[to].zone !== zone) to += delta
  if (to < 0 || to >= list.length) return
  ;[list[from], list[to]] = [list[to], list[from]]
}

/** JSON with object keys in one order: a dialog rebuilds what it saves. */
function canonical(v: unknown) {
  return JSON.stringify(v, (_, x) =>
    x && typeof x === 'object' && !Array.isArray(x)
      ? Object.fromEntries(
          Object.keys(x)
            .sort()
            .map((k) => [k, x[k]]),
        )
      : x,
  )
}
const same = (a: unknown, b: unknown) => canonical(a) === canonical(b)

/** How long the diff waits after the last edit before asking the server. */
const DIFF_DEBOUNCE_MS = 400
/** How long Undo stays on offer after a delete. */
const UNDO_MS = 8000

/**
 * Holds the saved configuration and an editable draft. Pages mutate the
 * draft; ApplyBar checks and applies it with a confirmation window.
 */
export const useConfigStore = defineStore('config', () => {
  const saved = ref<Config | null>(null)
  const draft = ref<Config | null>(null)
  const loaded = ref(false)
  const error = ref('')
  /**
   * Bumped every time an apply reaches the kernel. Pages that show live
   * state watch it and re-read, so a deleted VLAN leaves the interfaces
   * table without anyone pressing reload.
   */
  const applied = ref(0)
  /** The revision `saved` was read at; none before the first save. */
  const revision = ref('')
  /** The revision the draft was read from, which an apply names. */
  const base = ref('')

  const dirty = computed(() => !same(saved.value, draft.value))
  /**
   * The saved configuration moved on under a draft with edits of its own,
   * so the server would refuse to apply it.
   */
  const stale = computed(() => dirty.value && base.value !== revision.value)
  const zones = computed(() => draft.value?.zones ?? [])
  const interfaces = computed(() => draft.value?.interfaces ?? [])
  const aliases = computed(() => draft.value?.aliases ?? [])
  const rules = computed(() => draft.value?.rules ?? [])
  let undoing = false

  async function readSaved() {
    try {
      return await api.config.get()
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return { config: null, revision: 'none' }
      throw e
    }
  }

  async function load(force = false) {
    if (loaded.value && !force) return
    error.value = ''
    let next
    try {
      next = await readSaved()
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
      return
    }
    saved.value = next.config
    revision.value = next.revision
    base.value = next.revision
    draft.value = clone(saved.value)
    loaded.value = true
  }

  /**
   * Runs a mutation and reports it with a way back. Undo puts the draft
   * back exactly as it was before the mutation, so an edit made in the
   * few seconds the offer stands goes with it. That is simpler to trust
   * than a merge, and the toast says what it restores. One run inside
   * another is part of it: one toast, and Undo goes back past both. A run
   * that throws leaves the draft as it was.
   */
  function undoable(message: string, mutate: () => void) {
    if (undoing) {
      mutate()
      return
    }
    const before = clone(draft.value)
    const beforeBase = base.value
    undoing = true
    try {
      mutate()
    } catch (e) {
      draft.value = before
      throw e
    } finally {
      undoing = false
    }
    useToastStore().show(message, {
      timeout: UNDO_MS,
      action: {
        label: 'Undo',
        run: () => {
          draft.value = before
          base.value = beforeBase
        },
      },
    })
  }

  function discard() {
    if (!dirty.value) return
    undoable('Draft discarded.', () => {
      draft.value = clone(saved.value)
      base.value = revision.value
    })
  }

  /** After a confirmed apply, what was applied becomes the saved state. */
  function markSaved(cfg: Config | null = draft.value) {
    saved.value = clone(cfg)
  }

  /**
   * Reads the saved configuration again after something else committed
   * one: a confirm from another tab or from before a reload, or the wizard.
   * A draft with no edits of its own follows it, and `applied`, the draft
   * as this tab applied it, counts as no edits. A draft that does not
   * follow keeps its base and turns stale. Resolves true when the saved
   * configuration changed.
   */
  async function resync(applied: Config | null = null) {
    let next
    try {
      next = await readSaved()
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
      return false
    }
    error.value = ''
    const follow = !dirty.value || (applied !== null && same(draft.value, applied))
    revision.value = next.revision
    if (follow) base.value = next.revision
    if (same(next.config, saved.value)) return false
    saved.value = next.config
    if (follow) draft.value = clone(next.config)
    loaded.value = true
    return true
  }

  /** Called whenever the kernel has just been changed, applied or rolled back. */
  function markApplied() {
    applied.value++
  }

  /** Replace the draft wholesale, e.g. with an archived revision. */
  function replaceDraft(cfg: Config | null) {
    draft.value = clone(cfg)
  }

  function reset() {
    saved.value = null
    draft.value = null
    revision.value = ''
    base.value = ''
    loaded.value = false
  }

  // ---- interfaces ----------------------------------------------------

  function findInterface(name: string) {
    return interfaces.value.find((i) => i.name === name) ?? null
  }

  function upsertInterface(iface: Interface) {
    const list = draft.value.interfaces ?? (draft.value.interfaces = [])
    const idx = list.findIndex((i) => i.name === iface.name)
    if (idx === -1) list.push(clone(iface))
    else list[idx] = clone(iface)
  }

  /**
   * What goes with an interface when it is removed, in words, for the
   * dialog. Anything that names it means nothing without it: a DHCP
   * server, a gateway, a route, a DNS listener. A VLAN or PPPoE session on
   * top of it cannot exist without it. A bridge or bond carries on without
   * the members the delete takes, unless it takes them all, and an
   * interface delegated a prefix from it falls back to no IPv6. UPnP and
   * certificate addresses are weighed once, against every interface the
   * delete takes.
   */
  function interfaceDependents(name: string, going: Set<string> | null = null): string[] {
    const out = []
    const d = draft.value
    if (!d) return out
    const top = !going
    going ??= interfacesGoing(name)
    if (findInterface(name)?.wireguard) out.push(...peerDependents(name))
    for (const i of interfaces.value) {
      if (i.vlan?.parent === name || i.pppoe?.parent === name) {
        out.push(`${i.vlan ? 'VLAN' : 'PPPoE'} ${i.name}`, ...interfaceDependents(i.name, going))
      }
      const agg = i.bridge ?? i.bond
      if (agg?.members?.includes(name)) {
        if (!going.has(i.name)) out.push(`${i.name} loses member ${name}`)
        else if (agg.members.at(-1) === name) {
          const why = agg.members.length === 1 ? 'its only member' : 'left without members'
          out.push(`${i.bridge ? 'bridge' : 'bond'} ${i.name}, ${why}`)
          out.push(...interfaceDependents(i.name, going))
        }
      }
      if (i.ipv6?.delegatedFrom === name) out.push(`${i.name} loses its delegated IPv6 prefix`)
    }
    const dhcp: Partial<DHCPService> = d.services?.dhcp ?? {}
    if ((dhcp.servers ?? []).some((s) => s.interface === name)) out.push(`DHCP server on ${name}`)
    if ((dhcp.v6 ?? []).some((s) => s.interface === name)) {
      out.push(`IPv6 advertisement on ${name}`)
    }
    if ((d.services?.dns?.interfaces ?? []).includes(name)) out.push(`DNS listener on ${name}`)
    if ((d.services?.ntp?.interfaces ?? []).includes(name)) out.push(`time served on ${name}`)
    if ((d.services?.discovery?.interfaces ?? []).some((l) => l.interface === name)) {
      out.push(`discovery relay on ${name}`)
    }
    if (top) out.push(...addressDependents(going))
    for (const w of wolDevices.value) {
      if (w.interface === name) {
        out.push(`Wake on LAN device ${deviceName(w)}`, ...wolDeviceDependents(w.id))
      }
    }
    for (const r of ddnsRecords.value) {
      if (r.interface === name) out.push(`dynamic DNS record ${r.name}`)
    }
    const gone = new Set(gateways.value.filter((g) => going.has(g.interface)).map((g) => g.name))
    for (const g of gateways.value) {
      if (g.interface === name) out.push(`gateway ${g.name}`, ...gatewayDependents(g.name, gone))
    }
    for (const r of routes.value) if (r.interface === name) out.push(`route ${r.id}`)
    return out
  }

  /**
   * The interfaces a delete takes: the interface, what is stacked on it,
   * and a bridge or bond left without members.
   */
  function interfacesGoing(name: string) {
    const going = new Set([name])
    for (const n of going) {
      for (const i of interfaces.value) {
        const members = (i.bridge ?? i.bond)?.members ?? []
        if (
          i.vlan?.parent === n ||
          i.pppoe?.parent === n ||
          (members.includes(n) && members.every((m) => going.has(m)))
        )
          going.add(i.name)
      }
    }
    return going
  }

  /** What UPnP and the certificates lose with the interfaces going. */
  function addressDependents(going: Set<string>) {
    const out = []
    const upnp = draft.value.services?.upnp
    if (upnpStopsWith(going)) {
      out.push(
        going.has(upnp.externalInterface)
          ? 'UPnP, switched off without its external interface'
          : 'UPnP, switched off rather than answer every inside interface',
      )
    } else {
      for (const n of upnp?.interfaces ?? []) if (going.has(n)) out.push(`UPnP clients on ${n}`)
    }
    for (const c of certificates.value) {
      const lost = (c.interfaceAddresses ?? []).filter((n) => going.has(n))
      if (!lost.length) continue
      if (coversOnly(c, going)) {
        const only =
          lost.length > 1 ? `${lost.slice(0, -1).join(', ')} and ${lost.at(-1)}` : lost[0]
        out.push(`certificate ${c.id}, which covers only ${only}`)
        for (const x of certificateDependents(c.id)) {
          out.push(`${x} goes back to the built-in certificate`)
        }
      } else for (const n of lost) out.push(`certificate ${c.id} loses the address of ${n}`)
    }
    return out
  }

  /**
   * Whether UPnP has to stop with the interfaces going: it opens ports on
   * one of them, or it answers clients on them alone and an empty list
   * would mean every inside interface.
   */
  function upnpStopsWith(going: Set<string>) {
    const u = draft.value?.services?.upnp
    if (!u?.enabled) return false
    if (going.has(u.externalInterface)) return true
    return u.interfaces?.length > 0 && u.interfaces.every((n) => going.has(n))
  }

  /** An ordered certificate with nothing to cover once the interfaces go. */
  function coversOnly(cert: Certificate, going: Set<string>) {
    return (
      cert.source === 'acme' &&
      !cert.names?.length &&
      (cert.interfaceAddresses ?? []).every((n) => going.has(n))
    )
  }

  /** The mutation behind addressDependents. */
  function dropAddresses(going: Set<string>) {
    const upnp = draft.value.services?.upnp
    if (upnp) {
      if (upnpStopsWith(going)) upnp.enabled = false
      if (going.has(upnp.externalInterface)) delete upnp.externalInterface
      if (upnp.interfaces) {
        upnp.interfaces = upnp.interfaces.filter((n) => !going.has(n))
        if (!upnp.interfaces.length) delete upnp.interfaces
      }
    }
    for (const c of [...certificates.value]) {
      if (!c.interfaceAddresses?.some((n) => going.has(n))) continue
      if (coversOnly(c, going)) dropCertificate(c.id)
      else {
        c.interfaceAddresses = c.interfaceAddresses.filter((n) => !going.has(n))
        if (!c.interfaceAddresses.length) delete c.interfaceAddresses
      }
    }
  }

  /** The mutation behind removeInterface, in the order interfaceDependents lists it. */
  function dropInterface(name: string, going: Set<string> | null = null) {
    const d = draft.value
    const top = !going
    going ??= interfacesGoing(name)
    if (findInterface(name)?.wireguard) dropPeerReferences(name)
    for (const i of [...interfaces.value]) {
      if (i.vlan?.parent === name || i.pppoe?.parent === name) dropInterface(i.name, going)
      const agg = i.bridge ?? i.bond
      if (agg?.members?.includes(name)) {
        if (agg.members.length === 1) dropInterface(i.name, going)
        else {
          agg.members = agg.members.filter((m) => m !== name)
          if ('primary' in agg && agg.primary === name) delete agg.primary
        }
      }
      if (i.ipv6?.delegatedFrom === name) i.ipv6 = { mode: 'none' }
    }
    const dhcp = d.services?.dhcp
    if (dhcp?.servers) dhcp.servers = dhcp.servers.filter((s) => s.interface !== name)
    if (dhcp?.v6) dhcp.v6 = dhcp.v6.filter((s) => s.interface !== name)
    const dns = d.services?.dns
    if (dns?.interfaces) dns.interfaces = dns.interfaces.filter((n) => n !== name)
    const served = d.services?.ntp?.interfaces
    if (served) setNTP({ interfaces: served.filter((n) => n !== name) })
    const relayed = d.services?.discovery?.interfaces
    if (relayed?.some((l) => l.interface === name)) {
      setDiscovery({ interfaces: relayed.filter((l) => l.interface !== name) })
    }
    if (top) dropAddresses(going)
    for (const w of [...wolDevices.value]) if (w.interface === name) dropWoLDevice(w.id)
    for (const r of [...ddnsRecords.value]) if (r.interface === name) dropDdnsRecord(r.id)
    for (const g of [...gateways.value]) if (g.interface === name) dropGateway(g.name)
    if (d.routes) d.routes = d.routes.filter((r) => r.interface !== name)
    d.interfaces = interfaces.value.filter((i) => i.name !== name)
  }

  function removeInterface(name: string) {
    undoable(`Deleted ${name}.`, () => dropInterface(name))
  }

  // ---- WireGuard -------------------------------------------------------

  /** Tunnels are interfaces with a wireguard block. */
  const tunnels = computed(() => interfaces.value.filter((i) => i.wireguard))

  /** OpenVPN tunnels are interfaces with an openvpn block. */
  const openvpnTunnels = computed(() => interfaces.value.filter((i) => i.openvpn))

  /** The tailnet node, if this router has one. There is at most one. */
  const tailscale = computed(() => interfaces.value.find((i) => i.tailscale) ?? null)

  // ---- Wireless --------------------------------------------------------

  /** The radios this router is configured to use. */
  const radios = computed(() => draft.value?.wireless?.radios ?? [])

  /** Networks are interfaces with a wireless block. */
  const wirelessNetworks = computed(() => interfaces.value.filter((i) => i.wireless))

  /** The country every radio follows. */
  const wirelessCountry = computed(() => draft.value?.wireless?.country ?? '')

  function setWirelessCountry(code: string) {
    const w = draft.value.wireless ?? (draft.value.wireless = {})
    if (code) w.country = code
    else delete w.country
    if (!Object.keys(w).length) delete draft.value.wireless
  }

  function upsertRadio(radio: Radio) {
    const w = draft.value.wireless ?? (draft.value.wireless = {})
    const list = w.radios ?? (w.radios = [])
    const idx = list.findIndex((r) => r.name === radio.name)
    if (idx === -1) list.push(clone(radio))
    else list[idx] = clone(radio)
  }

  /** The networks a radio serves, which go with it. */
  function radioDependents(name: string) {
    return wirelessNetworks.value
      .filter((i) => i.wireless.radio === name)
      .map((i) => `network ${i.wireless.ssid} on ${i.name}`)
  }

  function removeRadio(name: string) {
    undoable(`Removed radio ${name}.`, () => {
      for (const i of [...wirelessNetworks.value])
        if (i.wireless.radio === name) dropInterface(i.name)
      const w = draft.value.wireless
      if (w?.radios) w.radios = w.radios.filter((r) => r.name !== name)
    })
  }

  function removeTunnel(name: string) {
    undoable(`Deleted tunnel ${name}.`, () => dropInterface(name))
  }

  function upsertPeer(tunnelName: string, peer: WireGuardPeer, previousName = peer.name) {
    const t = findInterface(tunnelName)
    if (!t?.wireguard) return
    const list = t.wireguard.peers ?? (t.wireguard.peers = [])
    const idx = list.findIndex((p) => p.name === previousName)
    if (idx === -1) list.push(clone(peer))
    else list[idx] = clone(peer)
    if (previousName === peer.name) return
    const from = `${tunnelName}/${previousName}`
    const to = `${tunnelName}/${peer.name}`
    for (const r of rules.value)
      for (const side of [r.source, r.destination]) if (side?.peer === from) side.peer = to
    for (const a of draft.value.services?.proxy?.access ?? [])
      if (a.source?.peer === from) a.source.peer = to
  }

  /** Whether an endpoint names the peer given, or with no peer any of the tunnel's. */
  function namesPeer(ep: Endpoint | undefined, tunnelName: string, peerName?: string) {
    if (!ep?.peer) return false
    return peerName ? ep.peer === `${tunnelName}/${peerName}` : ep.peer.startsWith(`${tunnelName}/`)
  }

  /**
   * Rules and proxy access lines that name a peer, or any peer of the
   * tunnel. They go with it, as a zone's rules go with the zone: a rule that
   * lost its peer would match everyone.
   */
  function peerDependents(tunnelName: string, peerName?: string) {
    const refs = []
    for (const r of rules.value)
      if (
        namesPeer(r.source, tunnelName, peerName) ||
        namesPeer(r.destination, tunnelName, peerName)
      )
        refs.push(`rule ${r.id}`)
    for (const a of draft.value?.services?.proxy?.access ?? [])
      if (namesPeer(a.source, tunnelName, peerName))
        refs.push(`proxy access rule ${a.description || a.id}`)
    return refs
  }

  /** The mutation behind peerDependents. */
  function dropPeerReferences(tunnelName: string, peerName?: string) {
    const d = draft.value
    if (d.rules)
      d.rules = d.rules.filter(
        (r) =>
          !namesPeer(r.source, tunnelName, peerName) &&
          !namesPeer(r.destination, tunnelName, peerName),
      )
    const proxy = d.services?.proxy
    if (proxy?.access)
      proxy.access = proxy.access.filter((a) => !namesPeer(a.source, tunnelName, peerName))
  }

  function removePeer(tunnelName: string, peerName: string) {
    const t = findInterface(tunnelName)
    if (!t?.wireguard) return
    undoable(`Deleted peer ${peerName}.`, () => {
      t.wireguard.peers = (t.wireguard.peers ?? []).filter((p) => p.name !== peerName)
      dropPeerReferences(tunnelName, peerName)
    })
  }

  // ---- zones -----------------------------------------------------------

  function upsertZone(zone: Zone, previousName = zone.name) {
    const list = draft.value.zones ?? (draft.value.zones = [])
    const idx = list.findIndex((z) => z.name === previousName)
    if (idx === -1) list.push(clone(zone))
    else list[idx] = clone(zone)
    if (previousName !== zone.name) renameZoneReferences(previousName, zone.name)
  }

  function renameZoneReferences(from: string, to: string) {
    for (const i of interfaces.value) if (i.zone === from) i.zone = to
    for (const r of rules.value) {
      if (r.zone === from) r.zone = to
      if (r.destZone === from) r.destZone = to
    }
    for (const pf of draft.value.nat?.portForwards ?? []) if (pf.zone === from) pf.zone = to
    for (const o of draft.value.nat?.outbound?.rules ?? []) if (o.zone === from) o.zone = to
    for (const o of draft.value.nat?.oneToOne ?? []) if (o.zone === from) o.zone = to
    for (const a of draft.value.services?.proxy?.access ?? []) if (a.zone === from) a.zone = to
    const defended = draft.value.protection?.zones
    if (defended) setProtectedZones(defended.map((z) => (z === from ? to : z)))
    const enforced = draft.value.blocking?.enforce?.zones
    if (enforced) setEnforcedZones(enforced.map((z) => (z === from ? to : z)))
  }

  /**
   * Interfaces assigned to a zone. They are the one thing that stops a
   * zone being deleted: an interface has to be somewhere, and moving it
   * is a decision only the admin can make.
   */
  function zoneInterfaces(name: string) {
    return interfaces.value.filter((i) => i.zone === name).map((i) => i.name)
  }

  /**
   * Rules and NAT entries written against a zone. They go with it when it
   * is deleted: none of them mean anything without their zone, and a rule
   * left pointing at a zone that is gone fails validation.
   */
  function zoneDependents(name: string) {
    const refs = []
    for (const r of rules.value)
      if (r.zone === name || r.destZone === name) refs.push(`rule ${r.id}`)
    for (const o of draft.value.nat?.outbound?.rules ?? [])
      if (o.zone === name) refs.push(`outbound NAT ${o.id}`)
    for (const pf of draft.value.nat?.portForwards ?? [])
      if (pf.zone === name) refs.push(`port forward ${pf.id}`)
    for (const o of draft.value.nat?.oneToOne ?? [])
      if (o.zone === name) refs.push(`1:1 NAT ${o.id}`)
    const defended = draft.value.protection?.zones ?? []
    if (defended.includes(name)) {
      refs.push(
        defended.some((z) => z !== name) ? 'protection' : 'protection, back to every external zone',
      )
    }
    const enforced = draft.value.blocking?.enforce?.zones ?? []
    if (enforced.includes(name)) {
      refs.push(
        enforced.some((z) => z !== name)
          ? 'DNS enforcement'
          : 'DNS enforcement, back to every zone that is not external',
      )
    }
    for (const a of draft.value.services?.proxy?.access ?? [])
      if (a.zone === name) refs.push(`proxy access rule ${a.description || a.id}`)
    return refs
  }

  /**
   * Deletes a zone and everything written against it. A rule that only
   * named the zone as its destination goes too rather than losing the
   * restriction: widening an allow rule to every destination is not
   * something a delete should do quietly.
   */
  function removeZone(name: string) {
    undoable(`Deleted zone ${name}.`, () => {
      draft.value.zones = zones.value.filter((z) => z.name !== name)
      draft.value.rules = rules.value.filter((r) => r.zone !== name && r.destZone !== name)
      const proxy = draft.value.services?.proxy
      if (proxy?.access) proxy.access = proxy.access.filter((a) => a.zone !== name)
      const defended = draft.value.protection?.zones
      if (defended) setProtectedZones(defended.filter((z) => z !== name))
      const enforced = draft.value.blocking?.enforce?.zones
      if (enforced) setEnforcedZones(enforced.filter((z) => z !== name))
      const n = draft.value.nat
      if (!n) return
      if (n.portForwards) n.portForwards = n.portForwards.filter((pf) => pf.zone !== name)
      if (n.outbound?.rules) n.outbound.rules = n.outbound.rules.filter((o) => o.zone !== name)
      if (n.oneToOne) n.oneToOne = n.oneToOne.filter((o) => o.zone !== name)
    })
  }

  // ---- rules -----------------------------------------------------------

  function rulesForZone(zone: string) {
    return rules.value.filter((r) => r.zone === zone)
  }

  function upsertRule(rule: Rule) {
    const list = draft.value.rules ?? (draft.value.rules = [])
    const idx = list.findIndex((r) => r.id === rule.id)
    if (idx === -1) list.push(clone(rule))
    else list[idx] = clone(rule)
  }

  function removeRule(id: string) {
    undoable(`Deleted rule ${id}.`, () => {
      draft.value.rules = rules.value.filter((r) => r.id !== id)
    })
  }

  /** Moves a rule up (-1) or down (+1) among the rules of its zone. */
  function moveRule(id: string, delta: number) {
    moveInZone(draft.value.rules, id, delta)
  }

  // ---- aliases ---------------------------------------------------------

  function upsertAlias(alias: Alias, previousName = alias.name) {
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
      const proxy = draft.value.services?.proxy
      for (const a of proxy?.access ?? [])
        if (a.source?.alias === previousName) a.source.alias = alias.name
      for (const s of proxy?.sites ?? [])
        if (s.allowFrom) s.allowFrom = s.allowFrom.map((f) => (f === previousName ? alias.name : f))
      const enforce = draft.value.blocking?.enforce
      if (enforce?.dohAlias === previousName) enforce.dohAlias = alias.name
      if (enforce?.exemptClients === previousName) enforce.exemptClients = alias.name
      if (enforce?.exemptDestinations === previousName) enforce.exemptDestinations = alias.name
    }
  }

  function aliasReferences(name: string) {
    const refs = []
    for (const r of rules.value) {
      if (r.source?.alias === name || r.source?.portAlias === name) refs.push(`rule ${r.id} source`)
      if (r.destination?.alias === name || r.destination?.portAlias === name)
        refs.push(`rule ${r.id} destination`)
    }
    const enforce = draft.value?.blocking?.enforce ?? {}
    if (enforce.dohAlias === name) refs.push('DNS blocking: DoH servers')
    if (enforce.exemptClients === name) refs.push('DNS blocking: exempt clients')
    if (enforce.exemptDestinations === name) refs.push('DNS blocking: exempt destinations')
    const proxy: Partial<Proxy> = draft.value?.services?.proxy ?? {}
    for (const a of proxy.access ?? [])
      if (a.source?.alias === name) refs.push(`proxy access rule ${a.description || a.id}`)
    for (const site of proxy.sites ?? [])
      if (site.allowFrom?.includes(name)) refs.push(`site ${site.id} allow from`)
    return refs
  }

  function removeAlias(name: string) {
    undoable(`Deleted alias ${name}.`, () => {
      draft.value.aliases = aliases.value.filter((a) => a.name !== name)
    })
  }

  // ---- NAT -------------------------------------------------------------

  const nat = computed<NAT>(() => draft.value?.nat ?? { outbound: { mode: 'automatic' } })

  function ensureNat() {
    if (!draft.value.nat) draft.value.nat = { outbound: { mode: 'automatic' } }
    if (!draft.value.nat.outbound) draft.value.nat.outbound = { mode: 'automatic' }
    return draft.value.nat
  }

  function setOutboundMode(mode: OutboundMode) {
    ensureNat().outbound.mode = mode
  }

  function upsertPortForward(pf: PortForward) {
    const n = ensureNat()
    const list = n.portForwards ?? (n.portForwards = [])
    const idx = list.findIndex((x) => x.id === pf.id)
    if (idx === -1) list.push(clone(pf))
    else list[idx] = clone(pf)
  }

  function removePortForward(id: string) {
    undoable(`Deleted port forward ${id}.`, () => {
      const n = ensureNat()
      n.portForwards = (n.portForwards ?? []).filter((x) => x.id !== id)
    })
  }

  function upsertOneToOne(entry: OneToOneNAT) {
    const n = ensureNat()
    const list = n.oneToOne ?? (n.oneToOne = [])
    const idx = list.findIndex((x) => x.id === entry.id)
    if (idx === -1) list.push(clone(entry))
    else list[idx] = clone(entry)
  }

  function removeOneToOne(id: string) {
    undoable(`Deleted 1:1 mapping ${id}.`, () => {
      const n = ensureNat()
      n.oneToOne = (n.oneToOne ?? []).filter((x) => x.id !== id)
    })
  }

  function upsertOutboundRule(rule: OutboundRule) {
    const n = ensureNat()
    const list = n.outbound.rules ?? (n.outbound.rules = [])
    const idx = list.findIndex((x) => x.id === rule.id)
    if (idx === -1) list.push(clone(rule))
    else list[idx] = clone(rule)
  }

  function removeOutboundRule(id: string) {
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

  function setShaping(name: string, shaping: Shaping) {
    const iface = findInterface(name)
    if (!iface) return
    iface.shaping = clone(shaping)
  }

  function clearShaping(name: string) {
    const iface = findInterface(name)
    if (!iface?.shaping) return
    undoable(`Deleted the speed set on ${name}.`, () => {
      delete findInterface(name).shaping
    })
  }

  /** Zones that hold back a host with a lot of connections open. */
  const busyZones = computed(() => zones.value.filter((z) => z.busy))

  function setBusy(zone: string, busy: BusyHosts) {
    const z = zones.value.find((x) => x.name === zone)
    if (!z) return
    z.busy = clone(busy)
  }

  function clearBusy(zone: string) {
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

  function upsertSchedule(schedule: Schedule, previousName = schedule.name) {
    const list = draft.value.schedules ?? (draft.value.schedules = [])
    const idx = list.findIndex((s) => s.name === previousName)
    if (idx === -1) list.push(clone(schedule))
    else list[idx] = clone(schedule)
    if (previousName !== schedule.name) {
      for (const r of rules.value) if (r.schedule === previousName) r.schedule = schedule.name
    }
  }

  function scheduleReferences(name: string) {
    return rules.value.filter((r) => r.schedule === name).map((r) => `rule ${r.id}`)
  }

  function removeSchedule(name: string) {
    undoable(`Deleted schedule ${name}.`, () => {
      draft.value.schedules = schedules.value.filter((s) => s.name !== name)
    })
  }

  // ---- services --------------------------------------------------------

  /** Returns the services block, creating defaults in the draft as needed. */
  function ensureServices() {
    const d = draft.value
    if (!d.services) d.services = { dhcp: { enabled: false }, dns: { enabled: false } } as Services
    if (!d.services.dhcp) d.services.dhcp = { enabled: false }
    if (!d.services.dns) d.services.dns = { enabled: false }
    return d.services
  }

  function upsertServer(server: DHCPServer) {
    const dhcp = ensureServices().dhcp
    const list = dhcp.servers ?? (dhcp.servers = [])
    const idx = list.findIndex((s) => s.interface === server.interface)
    if (idx === -1) list.push(clone(server))
    else list[idx] = clone(server)
  }

  function removeServer(iface: string) {
    undoable(`Deleted the DHCP server on ${iface}.`, () => {
      const dhcp = ensureServices().dhcp
      dhcp.servers = (dhcp.servers ?? []).filter((s) => s.interface !== iface)
    })
  }

  function upsertV6Server(server: DHCPv6Server) {
    const dhcp = ensureServices().dhcp
    const list = dhcp.v6 ?? (dhcp.v6 = [])
    const idx = list.findIndex((s) => s.interface === server.interface)
    if (idx === -1) list.push(clone(server))
    else list[idx] = clone(server)
  }

  function removeV6Server(iface: string) {
    undoable(`Stopped advertising IPv6 on ${iface}.`, () => {
      const dhcp = ensureServices().dhcp
      dhcp.v6 = (dhcp.v6 ?? []).filter((s) => s.interface !== iface)
    })
  }

  function upsertStaticLease(lease: StaticLease, previousMac = lease.mac) {
    const dhcp = ensureServices().dhcp
    const list = dhcp.staticLeases ?? (dhcp.staticLeases = [])
    const idx = list.findIndex((l) => l.mac.toLowerCase() === previousMac.toLowerCase())
    if (idx === -1) list.push(clone(lease))
    else list[idx] = clone(lease)
  }

  function removeStaticLease(mac: string) {
    undoable(`Deleted the static lease for ${mac}.`, () => {
      const dhcp = ensureServices().dhcp
      dhcp.staticLeases = (dhcp.staticLeases ?? []).filter(
        (l) => l.mac.toLowerCase() !== mac.toLowerCase(),
      )
    })
  }

  function upsertHostOverride(host: HostOverride, previousKey = overrideKey(host)) {
    const dns = ensureServices().dns
    const list = dns.hostOverrides ?? (dns.hostOverrides = [])
    const idx = list.findIndex((h) => overrideKey(h).toLowerCase() === previousKey.toLowerCase())
    if (idx === -1) list.push(clone(host))
    else list[idx] = clone(host)
  }

  function removeHostOverride(key: string) {
    undoable(`Deleted host override ${key}.`, () => {
      const dns = ensureServices().dns
      dns.hostOverrides = (dns.hostOverrides ?? []).filter(
        (h) => overrideKey(h).toLowerCase() !== key.toLowerCase(),
      )
    })
  }

  function upsertDomainOverride(override: DomainOverride, previousDomain = override.domain) {
    const dns = ensureServices().dns
    const list = dns.domainOverrides ?? (dns.domainOverrides = [])
    const idx = list.findIndex((d) => d.domain.toLowerCase() === previousDomain.toLowerCase())
    if (idx === -1) list.push(clone(override))
    else list[idx] = clone(override)
  }

  function removeDomainOverride(domain: string) {
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
  function upsertUPnPRule(rule: UPnPRule, index = -1) {
    const upnp = ensureUPnP()
    const list = upnp.acl ?? (upnp.acl = [])
    if (index < 0 || index >= list.length) list.push(clone(rule))
    else list[index] = clone(rule)
  }

  function removeUPnPRule(index: number) {
    undoable('Deleted the access list entry.', () => {
      const upnp = ensureUPnP()
      upnp.acl = (upnp.acl ?? []).filter((_, i) => i !== index)
    })
  }

  function moveUPnPRule(index: number, delta: number) {
    const list = ensureUPnP().acl ?? []
    const to = index + delta
    if (to < 0 || to >= list.length) return
    ;[list[index], list[to]] = [list[to], list[index]]
  }

  // ---- discovery relay -------------------------------------------------

  /**
   * The relay block as the draft has it over the default a router without
   * one reads as. Reading never writes the draft: changes go through
   * setDiscovery, so an untouched page leaves the draft as it was.
   */
  function ensureDiscovery() {
    return { ...DISCOVERY_DEFAULT, ...draft.value?.services?.discovery }
  }

  /**
   * Change the relay block. Empty lists and a log at its default are
   * dropped, and so is the block once it is the default again, unless the
   * saved configuration has one: then the change reads as the fields moved.
   * A field set to null is dropped.
   */
  function setDiscovery(patch: Record<string, unknown>) {
    const services = ensureServices()
    const block = { ...DISCOVERY_DEFAULT, ...services.discovery }
    for (const [k, v] of Object.entries(patch)) {
      if (v == null) delete block[k]
      else block[k] = clone(v)
    }
    if (!block.interfaces?.length) delete block.interfaces
    if (!block.services?.length) delete block.services
    if (!block.log?.entries) delete block.log
    const isDefault =
      Object.keys(block).length === Object.keys(DISCOVERY_DEFAULT).length &&
      Object.entries(DISCOVERY_DEFAULT).every(([k, v]) => block[k] === v)
    if (isDefault && !saved.value?.services?.discovery) delete services.discovery
    else services.discovery = block
  }

  // ---- time ------------------------------------------------------------

  /** The time block, or an empty one on a draft that has never had it. */
  const ntp = computed<NTP>(() => draft.value?.services?.ntp ?? {})

  /**
   * Change the time block. A field set to nothing is dropped, and so is
   * the block once it is empty, so a draft put back the way it was matches
   * the saved configuration again.
   */
  function setNTP(patch: Record<string, unknown>) {
    const services = ensureServices()
    const block = services.ntp ?? {}
    for (const [k, v] of Object.entries(patch)) {
      if (v === '' || v === false || v == null || (Array.isArray(v) && !v.length)) delete block[k]
      else block[k] = clone(v)
    }
    if (Object.keys(block).length) services.ntp = block
    else delete services.ntp
  }

  // ---- reverse proxy ---------------------------------------------------

  const proxy = computed<Partial<Proxy>>(() => draft.value?.services?.proxy ?? { enabled: false })

  function ensureProxy() {
    const services = ensureServices()
    if (!services.proxy) services.proxy = { enabled: false }
    return services.proxy
  }

  function setProxy(patch: Record<string, unknown>) {
    const services = ensureServices()
    const p = services.proxy ?? { enabled: false }
    for (const [k, v] of Object.entries(patch)) {
      // Always written while there is a block, so off stays in place.
      if (k === 'enabled') p.enabled = v === true
      else if (v === '' || v === false || v == null || (Array.isArray(v) && !v.length)) delete p[k]
      else p[k] = v
    }
    if (p.enabled || Object.keys(p).some((k) => k !== 'enabled')) services.proxy = p
    else delete services.proxy
  }

  /** upsert is the same shape for every list the proxy holds. */
  function upsertIn(key, entry, previousId = entry.id) {
    const p = ensureProxy()
    const list = p[key] ?? (p[key] = [])
    const idx = list.findIndex((e) => e.id === previousId)
    if (idx === -1) list.push(clone(entry))
    else list[idx] = clone(entry)
  }

  function removeFrom(key, id, message) {
    undoable(message, () => {
      const p = ensureProxy()
      p[key] = (p[key] ?? []).filter((e) => e.id !== id)
    })
  }

  function upsertPool(pool: ProxyPool, previousId = pool.id) {
    upsertIn('pools', pool, previousId)
    if (previousId === pool.id) return
    for (const s of ensureProxy().sites ?? []) {
      if (s.pool === previousId) s.pool = pool.id
      for (const p of s.paths ?? []) if (p.pool === previousId) p.pool = pool.id
    }
  }
  const upsertSite = (site: ProxySite, previousId = site.id) => upsertIn('sites', site, previousId)
  function upsertProfile(profile: WAFProfile, previousId = profile.id) {
    upsertIn('wafProfiles', profile, previousId)
    if (previousId === profile.id) return
    for (const s of ensureProxy().sites ?? []) if (s.waf === previousId) s.waf = profile.id
  }
  /**
   * Sites and paths that send to a pool. They stop it being deleted: a site
   * cannot exist without its pool.
   */
  function poolDependents(id: string) {
    const out = []
    for (const s of proxy.value.sites ?? []) {
      if (s.pool === id) out.push(`site ${s.id}`)
      for (const p of s.paths ?? []) if (p.pool === id) out.push(`site ${s.id} path ${p.prefix}`)
    }
    return out
  }

  /** Sites that would stop being inspected with a profile. */
  function profileDependents(id: string) {
    return (proxy.value.sites ?? []).filter((s) => s.waf === id).map((s) => `site ${s.id}`)
  }

  /** Present for symmetry with the other lists; nothing points at a site. */
  const siteDependents: (id: string) => string[] = () => []

  const removePool = (id: string) => removeFrom('pools', id, `Deleted pool ${id}.`)
  const removeSite = (id: string) => removeFrom('sites', id, `Deleted site ${id}.`)
  /** A deleted profile leaves the sites that named it uninspected. */
  function removeProfile(id: string) {
    undoable(`Deleted WAF profile ${id}.`, () => {
      const p = ensureProxy()
      p.wafProfiles = (p.wafProfiles ?? []).filter((w) => w.id !== id)
      for (const s of p.sites ?? []) if (s.waf === id) delete s.waf
    })
  }

  // ---- reverse proxy access ---------------------------------------------

  /** Adds an access rule, or replaces the one with its id. */
  const upsertProxyAccess = (line: ProxyAccess) => upsertIn('access', line)
  const removeProxyAccess = (id: string) => removeFrom('access', id, 'Deleted the access rule.')

  /** Moves an access rule up (-1) or down (+1) among the rules of its zone. */
  function moveProxyAccess(id: string, delta: number) {
    moveInZone(ensureProxy().access ?? [], id, delta)
  }

  /** The draft's profile, to change. */
  const draftProfile = (id: string) => (ensureProxy().wafProfiles ?? []).find((w) => w.id === id)

  /** Switches a rule off for a profile, or says it is off already. */
  function addExclusion(profileId: string, exclusion: WAFExclusion) {
    const profile = (proxy.value.wafProfiles ?? []).find((w) => w.id === profileId)
    if (!profile) return
    const text = exclusionText(exclusion)
    if ((profile.exclusions ?? []).some((e) => sameExclusion(e, exclusion))) {
      useToastStore().show(`${text[0].toUpperCase()}${text.slice(1)} is excluded already.`)
      return
    }
    undoable(`Excluded ${text} in the draft.`, () => {
      const w = draftProfile(profileId)
      const list = w.exclusions ?? (w.exclusions = [])
      list.push(clone(exclusion))
    })
  }

  /** An exclusion by its place in the profile's list. */
  function updateExclusion(profileId: string, index: number, exclusion: WAFExclusion) {
    const list = draftProfile(profileId)?.exclusions
    if (list?.[index]) list[index] = clone(exclusion)
  }

  function removeExclusion(profileId: string, index: number) {
    const e = (proxy.value.wafProfiles ?? []).find((w) => w.id === profileId)?.exclusions?.[index]
    if (!e) return
    undoable(`Deleted the exclusion of ${exclusionText(e)}.`, () => {
      const w = draftProfile(profileId)
      w.exclusions.splice(index, 1)
      if (!w.exclusions.length) delete w.exclusions
    })
  }

  // ---- gateways --------------------------------------------------------

  const gateways = computed(() => draft.value?.gateways ?? [])

  function upsertGateway(gateway: Gateway, previousName = gateway.name) {
    const list = draft.value.gateways ?? (draft.value.gateways = [])
    const idx = list.findIndex((g) => g.name === previousName)
    if (idx === -1) list.push(clone(gateway))
    else list[idx] = clone(gateway)
    if (previousName === gateway.name) return
    for (const g of gatewayGroups.value)
      for (const m of g.members ?? []) if (m.gateway === previousName) m.gateway = gateway.name
    renameRouteTarget(previousName, gateway.name)
  }

  /** Rules and this router's lookups follow a gateway or group to its new name. */
  function renameRouteTarget(from: string, to: string) {
    for (const r of rules.value) if (r.gateway === from) r.gateway = to
    const dns = draft.value.services?.dns
    if (dns?.via === from) dns.via = to
  }

  /** What losing a gateway or group does to this router's own lookups. */
  function lookupsDependent(name: string) {
    return draft.value?.services?.dns?.via === name
      ? ["this router's lookups go back to the default route"]
      : []
  }

  const gatewayGroups = computed(() => draft.value?.gatewayGroups ?? [])

  /** Rules routed through a group, which lose that when it goes. */
  function groupDependents(name: string) {
    return [
      ...rules.value.filter((r) => r.gateway === name).map((r) => `rule ${r.id} loses its gateway`),
      ...lookupsDependent(name),
    ]
  }

  /**
   * What changes when a gateway goes: rules routed through it fall back
   * to the default route, groups carry on without it, and a group left
   * without members goes too.
   */
  function gatewayDependents(name: string, going = new Set([name])): string[] {
    const out = [
      ...rules.value.filter((r) => r.gateway === name).map((r) => `rule ${r.id} loses its gateway`),
      ...lookupsDependent(name),
    ]
    for (const g of gatewayGroups.value) {
      const members = g.members ?? []
      if (!members.some((m) => m.gateway === name)) continue
      if (!members.every((m) => going.has(m.gateway)))
        out.push(`group ${g.name} loses member ${name}`)
      else if (members.at(-1).gateway === name) {
        const why = members.length === 1 ? 'its only member' : 'left without members'
        out.push(`group ${g.name}, ${why}`, ...groupDependents(g.name))
      }
    }
    return out
  }

  /** Rules and this router's lookups routed through a gateway or group go back to the default route. */
  function dropRouteTarget(name: string) {
    for (const r of rules.value) if (r.gateway === name) delete r.gateway
    const dns = draft.value.services?.dns
    if (dns?.via === name) delete dns.via
  }

  function dropGatewayGroup(name: string) {
    dropRouteTarget(name)
    draft.value.gatewayGroups = gatewayGroups.value.filter((g) => g.name !== name)
  }

  function dropGateway(name: string) {
    dropRouteTarget(name)
    for (const g of [...gatewayGroups.value]) {
      const members = g.members ?? []
      if (!members.some((m) => m.gateway === name)) continue
      if (members.length === 1) dropGatewayGroup(g.name)
      else g.members = members.filter((m) => m.gateway !== name)
    }
    draft.value.gateways = gateways.value.filter((g) => g.name !== name)
  }

  function removeGateway(name: string) {
    undoable(`Deleted gateway ${name}.`, () => dropGateway(name))
  }

  /** Everything rules can route through: gateways first, then groups. */
  const routeTargets = computed(() => [
    ...gateways.value.map((g) => ({ name: g.name, kind: 'gateway', enabled: g.enabled })),
    ...gatewayGroups.value.map((g) => ({ name: g.name, kind: 'group', enabled: g.enabled })),
  ])

  function upsertGatewayGroup(group: GatewayGroup, previousName = group.name) {
    const list = draft.value.gatewayGroups ?? (draft.value.gatewayGroups = [])
    const idx = list.findIndex((g) => g.name === previousName)
    if (idx === -1) list.push(clone(group))
    else list[idx] = clone(group)
    if (previousName !== group.name) renameRouteTarget(previousName, group.name)
  }

  function removeGatewayGroup(name: string) {
    undoable(`Deleted gateway group ${name}.`, () => dropGatewayGroup(name))
  }

  // ---- updates ---------------------------------------------------------

  /**
   * The update settings, created on first use. An empty block means the
   * defaults, which the daemon fills in: security fixes, weekly.
   */
  function ensureUpdates() {
    const d = draft.value
    if (!d.updates) d.updates = {} as Updates
    if (!d.updates.system) d.updates.system = {}
    if (!d.updates.ostiole) d.updates.ostiole = {}
    return d.updates
  }

  const updates = computed<Partial<Updates>>(() => draft.value?.updates ?? {})
  const systemUpdates = computed<PackageUpdates>(() => updates.value.system ?? {})
  const ostioleUpdates = computed<SelfUpdates>(() => updates.value.ostiole ?? {})

  /** A field set to nothing is dropped, as the saved configuration has it. */
  function setUpdates(which: 'system' | 'ostiole', patch: Record<string, unknown>) {
    const block = ensureUpdates()[which]
    for (const [k, v] of Object.entries(patch)) {
      if (v === '' || v == null || (Array.isArray(v) && !v.length)) delete block[k]
      else block[k] = v
    }
  }

  // ---- backup -----------------------------------------------------------

  /** The backup settings, created on first use. */
  function ensureBackup() {
    const d = draft.value
    if (!d.backup) d.backup = {}
    if (!d.backup.remote) d.backup.remote = {} as RemoteBackup
    return d.backup
  }

  /** The bucket the configuration is copied to, or an empty one. */
  const remoteBackup = computed<Partial<RemoteBackup>>(() => draft.value?.backup?.remote ?? {})

  /**
   * A field set to nothing is dropped, and the block once nothing in it
   * is on or set, as the saved configuration has it.
   */
  function setRemoteBackup(patch: Record<string, unknown>) {
    const backup = ensureBackup()
    const remote = backup.remote
    for (const [k, v] of Object.entries(patch)) {
      if (k !== 'enabled' && (v === '' || v === 0 || v == null)) delete remote[k]
      else remote[k] = v
    }
    if (!Object.keys(remote).some((k) => k !== 'enabled' || remote.enabled)) delete backup.remote
    if (!Object.keys(backup).length) delete draft.value.backup
  }

  // ---- notifications ---------------------------------------------------

  /** Where notices go, or an empty block on a draft that has never had one. */
  const notifications = computed<Notifications>(() => draft.value?.notifications ?? {})

  /**
   * Change the notifications block, or its email or webhook part. A field
   * set to nothing is dropped, and so is a part left empty, so a draft put
   * back the way it was matches the saved configuration again.
   */
  function setNotifications(patch: Record<string, unknown>, part?: 'email' | 'webhook') {
    const d = draft.value
    const block = d.notifications ?? {}
    const target = part ? (block[part] ?? {}) : block
    for (const [k, v] of Object.entries(patch)) {
      if (v === '' || v === false || v == null || (Array.isArray(v) && !v.length)) delete target[k]
      else target[k] = v
    }
    if (part) {
      if (Object.keys(target).length) block[part] = target
      else delete block[part]
    }
    if (Object.keys(block).length) d.notifications = block
    else delete d.notifications
  }

  // ---- protection ------------------------------------------------------

  /** The edge defence, or an empty one on a draft that has never had it. */
  const protection = computed<Protection>(() => draft.value?.protection ?? {})

  /**
   * Turn one defence on or off. A limit is an object or nothing at all:
   * the renderer reads "is it there" rather than an enabled flag, so
   * switching one off removes it. A null value switches the limit off.
   */
  function setDefence(
    which: 'synFlood' | 'icmpFlood' | 'portScan',
    value: Record<string, unknown> | null,
  ) {
    if (value === null) {
      dropProtection(which)
      return
    }
    const p = draft.value.protection ?? (draft.value.protection = {})
    const next = { ...(p[which] ?? {}) }
    for (const [k, v] of Object.entries(value)) {
      if (v == null) delete next[k]
      else next[k] = clone(v)
    }
    Object.assign(p, { [which]: next })
  }

  /** The zones defended; an empty list means every external zone. */
  function setProtectedZones(names: string[]) {
    if (!names.length) {
      dropProtection('zones')
      return
    }
    const p = draft.value.protection ?? (draft.value.protection = {})
    p.zones = [...names]
  }

  /** Go leaves an empty protection block out, so the draft does too. */
  function dropProtection(key: keyof Protection) {
    const p = draft.value.protection
    if (!p) return
    delete p[key]
    if (!Object.keys(p).length) delete draft.value.protection
  }

  // ---- crons -----------------------------------------------------------

  const crons = computed(() => draft.value?.crons ?? [])

  function upsertCron(cron: Cron) {
    const list = draft.value.crons ?? (draft.value.crons = [])
    const idx = list.findIndex((c) => c.id === cron.id)
    if (idx === -1) list.push(clone(cron))
    else list[idx] = clone(cron)
  }

  function removeCron(id: string) {
    undoable(`Deleted cron job ${id}.`, () => {
      draft.value.crons = crons.value.filter((c) => c.id !== id)
    })
  }

  // ---- wake on LAN -----------------------------------------------------

  /** The machines this router can wake, or none on a draft that never had any. */
  const wolDevices = computed(() => draft.value?.services?.wol?.devices ?? [])

  /**
   * Writes the device list. The block goes once it is empty, so a draft put
   * back the way it was matches the saved configuration again.
   */
  function setWoLDevices(list: WoLDevice[]) {
    const services = ensureServices()
    const wol = { ...services.wol }
    if (list.length) wol.devices = list
    else delete wol.devices
    if (Object.keys(wol).length) services.wol = wol
    else delete services.wol
  }

  function upsertWoLDevice(device: WoLDevice, previousId = device.id) {
    const list = [...wolDevices.value]
    const idx = list.findIndex((d) => d.id === previousId)
    if (idx === -1) list.push(clone(device))
    else list[idx] = clone(device)
    setWoLDevices(list)
  }

  /** The wake cron jobs that name a device, which go with it. */
  function wolDeviceDependents(id: string) {
    return crons.value
      .filter((c) => c.kind === 'wake' && c.device === id)
      .map((c) => `cron job ${c.description || c.id}`)
  }

  function dropWoLDevice(id: string) {
    const kept = crons.value.filter((c) => !(c.kind === 'wake' && c.device === id))
    if (kept.length !== crons.value.length) draft.value.crons = kept
    setWoLDevices(wolDevices.value.filter((d) => d.id !== id))
  }

  function removeWoLDevice(device: WoLDevice) {
    undoable(`Deleted ${deviceName(device)}.`, () => dropWoLDevice(device.id))
  }

  // ---- dynamic DNS -----------------------------------------------------

  /** The dynamic DNS records, or none on a draft that never had any. */
  const ddnsRecords = computed(() => draft.value?.services?.ddns?.records ?? [])

  /**
   * Writes the record list. The block goes once it is empty, so a draft put
   * back the way it was matches the saved configuration again.
   */
  function setDdnsRecords(list: DDNSRecord[]) {
    const services = ensureServices()
    const ddns = { ...services.ddns }
    if (list.length) ddns.records = list
    else delete ddns.records
    if (Object.keys(ddns).length) services.ddns = ddns
    else delete services.ddns
  }

  function upsertDdnsRecord(record: DDNSRecord, previousId = record.id) {
    const list = [...ddnsRecords.value]
    const idx = list.findIndex((r) => r.id === previousId)
    if (idx === -1) list.push(clone(record))
    else list[idx] = clone(record)
    setDdnsRecords(list)
  }

  function dropDdnsRecord(id: string) {
    setDdnsRecords(ddnsRecords.value.filter((r) => r.id !== id))
  }

  function removeDdnsRecord(record: DDNSRecord) {
    undoable(`Deleted ${record.name}.`, () => dropDdnsRecord(record.id))
  }

  // ---- certificates ----------------------------------------------------

  const certificates = computed(() => draft.value?.certificates ?? [])
  const acmeAccounts = computed(() => draft.value?.acme?.accounts ?? [])
  const dnsProviders = computed(() => draft.value?.dnsProviders ?? [])

  function ensureACME() {
    const d = draft.value
    if (!d.acme) d.acme = {}
    return d.acme
  }

  /** A renamed certificate keeps the sites and the web UI that serve it. */
  function upsertCertificate(cert: Certificate, was = cert.id) {
    const list = draft.value.certificates ?? (draft.value.certificates = [])
    const idx = list.findIndex((c) => c.id === was)
    if (idx === -1) list.push(clone(cert))
    else list[idx] = clone(cert)
    if (was === cert.id) return
    const m = draft.value.system?.management
    if (m?.certificate === was) m.certificate = cert.id
    for (const s of draft.value.services?.proxy?.sites ?? [])
      if (s.certificate === was) s.certificate = cert.id
  }

  /** The mutation behind removeCertificate, in the order certificateDependents lists it. */
  function dropCertificate(id: string) {
    const d = draft.value
    d.certificates = certificates.value.filter((c) => c.id !== id)
    if (d.system?.management?.certificate === id) delete d.system.management.certificate
    for (const s of d.services?.proxy?.sites ?? []) if (s.certificate === id) delete s.certificate
  }

  function removeCertificate(id: string) {
    undoable(`Deleted certificate ${id}.`, () => dropCertificate(id))
  }

  /** What goes back to the built-in certificate when this one goes. */
  function certificateDependents(id: string) {
    const out = draft.value?.system?.management?.certificate === id ? ['the web UI'] : []
    for (const s of proxy.value.sites ?? []) if (s.certificate === id) out.push(`site ${s.id}`)
    return out
  }

  function upsertAcmeAccount(account: ACMEAccount, was = account.id) {
    const acme = ensureACME()
    const list = acme.accounts ?? (acme.accounts = [])
    const idx = list.findIndex((a) => a.id === was)
    if (idx === -1) list.push(clone(account))
    else list[idx] = clone(account)
  }

  function removeAcmeAccount(id: string) {
    undoable(`Deleted ACME account ${id}.`, () => {
      ensureACME().accounts = acmeAccounts.value.filter((a) => a.id !== id)
    })
  }

  /** The certificates ordered from this account. */
  function accountDependents(id: string) {
    return certificates.value.filter((c) => c.account === id).map((c) => c.id)
  }

  function upsertDnsProvider(provider: DNSProvider, was = provider.id) {
    const list = draft.value.dnsProviders ?? (draft.value.dnsProviders = [])
    const idx = list.findIndex((p) => p.id === was)
    if (idx === -1) list.push(clone(provider))
    else list[idx] = clone(provider)
  }

  function removeDnsProvider(id: string) {
    undoable(`Deleted DNS provider ${id}.`, () => {
      const kept = dnsProviders.value.filter((p) => p.id !== id)
      if (kept.length) draft.value.dnsProviders = kept
      else delete draft.value.dnsProviders
    })
  }

  /**
   * What writes through this provider: the dns-01 certificates that name it
   * or whose names it holds, and the dynamic DNS records under its domains.
   */
  function providerDependents(id: string) {
    const automatic = (c) =>
      !c.provider &&
      c.source === 'acme' &&
      c.challenge === 'dns-01' &&
      certificateProvider(dnsProviders.value, c).provider?.id === id
    const out = certificates.value.filter((c) => c.provider === id || automatic(c)).map((c) => c.id)
    for (const r of ddnsRecords.value) {
      if (providerFor(dnsProviders.value, r.name)?.provider.id === id) out.push(r.name)
    }
    return out
  }

  /** Which certificate the web UI serves; empty is the built-in one. */
  function setManagementCertificate(id: string) {
    const m = draft.value.system.management ?? (draft.value.system.management = {} as Management)
    if (id) m.certificate = id
    else delete m.certificate
  }

  // ---- routes ----------------------------------------------------------

  const routes = computed(() => draft.value?.routes ?? [])

  function upsertRoute(route: StaticRoute) {
    const list = draft.value.routes ?? (draft.value.routes = [])
    const idx = list.findIndex((r) => r.id === route.id)
    if (idx === -1) list.push(clone(route))
    else list[idx] = clone(route)
  }

  function removeRoute(id: string) {
    undoable(`Deleted route ${id}.`, () => {
      draft.value.routes = routes.value.filter((r) => r.id !== id)
    })
  }

  // ---- DNS blocking ----------------------------------------------------

  const blocking = computed<Partial<Blocking>>(() => draft.value?.blocking ?? { enabled: false })

  function ensureBlocking() {
    const d = draft.value
    if (!d.blocking) d.blocking = { enabled: false } as Blocking
    if (!d.blocking.enforce) d.blocking.enforce = {}
    return d.blocking
  }

  /** The zones DNS enforcement covers; an empty list means every zone that is not external. */
  function setEnforcedZones(names: string[]) {
    const e = ensureBlocking().enforce
    if (!names.length) {
      delete e.zones
      return
    }
    e.zones = [...names]
  }

  const blockLists = computed(() => draft.value?.blocking?.lists ?? [])

  function upsertBlockList(list: BlockList, previousName = list.name) {
    const b = ensureBlocking()
    const all = b.lists ?? (b.lists = [])
    const idx = all.findIndex((l) => l.name === previousName)
    if (idx === -1) all.push(clone(list))
    else all[idx] = clone(list)
  }

  function removeBlockList(name: string) {
    undoable(`Deleted block list ${name}.`, () => {
      const b = ensureBlocking()
      b.lists = (b.lists ?? []).filter((l) => l.name !== name)
    })
  }

  /**
   * Puts a name on the allow or the deny list, or takes it off. Going on
   * one takes it off the other: allow wins, so a name on both would read
   * as blocked and never be. A list left empty is dropped, so a toggle
   * put back leaves the draft as it was.
   */
  function setException(name: string, key: 'allow' | 'deny', on: boolean) {
    const b = ensureBlocking()
    const n = normalizeName(name)
    const without = (k) => {
      const list = b[k] ?? []
      const kept = list.filter((e) => normalizeName(e) !== n)
      if (kept.length === list.length) return
      if (kept.length) b[k] = kept
      else delete b[k]
    }
    if (!on) {
      without(key)
      return
    }
    without(key === 'allow' ? 'deny' : 'allow')
    const list = b[key] ?? []
    if (!list.some((e) => normalizeName(e) === n)) b[key] = [...list, n]
  }

  // ---- what the draft changes ------------------------------------------

  /**
   * The draft compared with the saved configuration, as the server sees
   * it. Refreshed a moment after the last edit. It is a nicety: the apply
   * bar works without it, so a failed diff is not an error anyone sees.
   */
  const changes = ref<ConfigChange[]>([])
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
      if (seq === diffSeq) changes.value = out as ConfigChange[]
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
  function sectionFor(path: string) {
    const [, key, id] = /^([A-Za-z0-9]+)(?:\[([^\]]*)\])?/.exec(path) ?? []
    switch (key) {
      case 'interfaces': {
        // A speed lives on the interface but is edited on its own page, so
        // the change is shown where it was made.
        if (path.includes('.shaping')) return '/traffic/shaping'
        const i =
          interfaces.value.find((x) => x.name === id) ??
          saved.value?.interfaces?.find((x) => x.name === id)
        if (i?.wireguard) return '/vpn/wireguard'
        if (i?.openvpn) return '/vpn/openvpn'
        if (i?.tailscale) return '/vpn/tailscale'
        if (i?.wireless) return '/wireless'
        return '/interfaces'
      }
      case 'wireless':
        return '/wireless'
      case 'vpn': {
        if (path.startsWith('vpn.wireguardLog')) return '/vpn/wireguard'
        if (path.startsWith('vpn.tailscaleLog')) return '/vpn/tailscale'
        // The block is added or removed whole, so its logs say whose it is.
        const vpn = draft.value?.vpn ?? saved.value?.vpn ?? {}
        if (!vpn.tailscaleLog) return '/vpn/wireguard'
        if (!vpn.wireguardLog) return '/vpn/tailscale'
        return '/vpn'
      }
      case 'traffic':
        return '/traffic/usage'
      case 'zones':
        // Holding back a busy host is a priority decision, so it is edited
        // and shown where the other priorities are.
        return path.includes('.busy') ? '/traffic/shaping' : '/interfaces'
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
        if (path.startsWith('services.dhcp')) return '/services/dhcp'
        if (path.startsWith('services.dns')) return '/services/dns'
        if (path.startsWith('services.upnp')) return '/services/upnp'
        if (path.startsWith('services.ntp')) return '/services/time'
        if (path.startsWith('services.wol')) return '/services/wol'
        if (path.startsWith('services.ddns')) return '/services/ddns'
        if (path.startsWith('services.proxy')) return '/services/proxy'
        if (path.startsWith('services.discovery')) return '/services/discovery'
        return '/services'
      case 'blocking':
        return '/services/dns'
      case 'crons':
        return '/system/crons'
      case 'updates':
        return '/system/updates'
      case 'notifications':
        return '/system/notifications'
      case 'backup':
        return '/system/configuration'
      case 'acme':
      case 'certificates':
        return '/system/certificates'
      case 'dnsProviders':
        return '/system/dns-providers'
      case 'system':
        if (path.startsWith('system.keepRevisions')) return '/system/configuration'
        if (path.startsWith('system.keepDriveReadings')) return '/diagnostics/drives'
        if (path.startsWith('system.conntrackMax')) return '/firewall/protection'
        if (path.startsWith('system.management.certificate')) return '/system/certificates'
        if (
          path.startsWith('system.management.firewallLog') ||
          path.startsWith('system.management.logDefaultDrops')
        )
          return '/firewall/log'
        return '/system/general'
      default:
        return '/system/general'
    }
  }

  const changedPaths = computed(() => new Set(changes.value.map((c) => sectionFor(c.path))))

  /** Whether a sidebar item has anything unapplied under it. */
  function hasChanges(to: string) {
    for (const p of changedPaths.value) if (p === to || p.startsWith(`${to}/`)) return true
    return false
  }

  /**
   * Whether the draft touches one entry of a named list, for marking its
   * row: `isChanged('rules', 'web-in')`, `isChanged('nat.portForwards', id)`.
   */
  function isChanged(list: string, key: string | number) {
    const entry = `${list}[${key}]`
    return changes.value.some(
      (c) => c.path === entry || c.path.startsWith(`${entry}.`) || c.path.startsWith(`${entry}[`),
    )
  }

  return {
    changes,
    hasChanges,
    isChanged,
    sectionFor,
    interfaceDependents,
    removeTunnel,
    gatewayDependents,
    groupDependents,
    saved,
    draft,
    base,
    stale,
    loaded,
    error,
    dirty,
    applied,
    zones,
    interfaces,
    ntp,
    setNTP,
    ensureDiscovery,
    setDiscovery,
    aliases,
    rules,
    protection,
    setDefence,
    setProtectedZones,
    load,
    discard,
    undoable,
    markSaved,
    resync,
    markApplied,
    replaceDraft,
    reset,
    findInterface,
    upsertInterface,
    removeInterface,
    tunnels,
    openvpnTunnels,
    tailscale,
    radios,
    wirelessNetworks,
    wirelessCountry,
    setWirelessCountry,
    upsertRadio,
    radioDependents,
    removeRadio,
    upsertPeer,
    removePeer,
    peerDependents,
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
    setEnforcedZones,
    blockLists,
    upsertBlockList,
    removeBlockList,
    setException,
    upsertOutboundRule,
    removeOutboundRule,
    routes,
    certificates,
    acmeAccounts,
    dnsProviders,
    upsertCertificate,
    removeCertificate,
    certificateDependents,
    upsertAcmeAccount,
    removeAcmeAccount,
    accountDependents,
    upsertDnsProvider,
    removeDnsProvider,
    providerDependents,
    setManagementCertificate,
    crons,
    upsertCron,
    removeCron,
    wolDevices,
    upsertWoLDevice,
    removeWoLDevice,
    wolDeviceDependents,
    ddnsRecords,
    upsertDdnsRecord,
    removeDdnsRecord,
    updates,
    systemUpdates,
    ostioleUpdates,
    setUpdates,
    remoteBackup,
    setRemoteBackup,
    notifications,
    setNotifications,
    gateways,
    upsertGateway,
    removeGateway,
    gatewayGroups,
    routeTargets,
    upsertGatewayGroup,
    removeGatewayGroup,
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
    proxy,
    setProxy,
    upsertPool,
    removePool,
    poolDependents,
    upsertSite,
    removeSite,
    siteDependents,
    upsertProfile,
    removeProfile,
    profileDependents,
    upsertProxyAccess,
    removeProxyAccess,
    moveProxyAccess,
    addExclusion,
    updateExclusion,
    removeExclusion,
  }
})
