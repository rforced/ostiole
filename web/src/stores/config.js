import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { ApiError, api } from '@/lib/api'

const clone = (v) => (v === null || v === undefined ? v : JSON.parse(JSON.stringify(v)))

/**
 * Holds the saved configuration and an editable draft. Pages mutate the
 * draft; ApplyBar checks and applies it with a confirmation window.
 */
export const useConfigStore = defineStore('config', () => {
  const saved = ref(null)
  const draft = ref(null)
  const loaded = ref(false)
  const error = ref('')

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

  function discard() {
    draft.value = clone(saved.value)
  }

  /** After a confirmed apply the draft becomes the saved state. */
  function markSaved() {
    saved.value = clone(draft.value)
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

  function removeInterface(name) {
    draft.value.interfaces = interfaces.value.filter((i) => i.name !== name)
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
  }

  /** Names of things that reference a zone; empty when it can be deleted. */
  function zoneReferences(name) {
    const refs = []
    for (const i of interfaces.value) if (i.zone === name) refs.push(`interface ${i.name}`)
    for (const r of rules.value)
      if (r.zone === name || r.destZone === name) refs.push(`rule ${r.id}`)
    for (const pf of draft.value.nat?.portForwards ?? [])
      if (pf.zone === name) refs.push(`port forward ${pf.id}`)
    for (const o of draft.value.nat?.outbound?.rules ?? [])
      if (o.zone === name) refs.push(`outbound NAT ${o.id}`)
    return refs
  }

  function removeZone(name) {
    draft.value.zones = zones.value.filter((z) => z.name !== name)
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
    draft.value.rules = rules.value.filter((r) => r.id !== id)
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
    return refs
  }

  function removeAlias(name) {
    draft.value.aliases = aliases.value.filter((a) => a.name !== name)
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
    const n = ensureNat()
    n.portForwards = (n.portForwards ?? []).filter((x) => x.id !== id)
  }

  function upsertOutboundRule(rule) {
    const n = ensureNat()
    const list = n.outbound.rules ?? (n.outbound.rules = [])
    const idx = list.findIndex((x) => x.id === rule.id)
    if (idx === -1) list.push(clone(rule))
    else list[idx] = clone(rule)
  }

  function removeOutboundRule(id) {
    const n = ensureNat()
    n.outbound.rules = (n.outbound.rules ?? []).filter((x) => x.id !== id)
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
    draft.value.routes = routes.value.filter((r) => r.id !== id)
  }

  return {
    saved,
    draft,
    loaded,
    error,
    dirty,
    zones,
    interfaces,
    aliases,
    rules,
    load,
    discard,
    markSaved,
    reset,
    findInterface,
    upsertInterface,
    removeInterface,
    upsertZone,
    zoneReferences,
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
    upsertOutboundRule,
    removeOutboundRule,
    routes,
    upsertRoute,
    removeRoute,
  }
})
