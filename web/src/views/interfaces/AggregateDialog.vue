<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** "bridge" or "bond". */
  kind: { type: String, default: 'bridge' },
  /** Live links that could be members. */
  candidates: { type: Array, default: () => [] },
  /** The interface being edited, or null for a new one. */
  iface: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const error = ref('')
const form = ref(blank())

const BOND_MODES = [
  { value: 'active-backup', label: 'Active/backup (one link at a time, no switch setup)' },
  { value: '802.3ad', label: 'LACP 802.3ad (the switch must be configured to match)' },
  { value: 'balance-rr', label: 'Round robin (packets in turn)' },
  { value: 'balance-xor', label: 'Balance XOR (a flow always takes the same link)' },
  { value: 'broadcast', label: 'Broadcast (everything on every link)' },
  { value: 'balance-tlb', label: 'Adaptive transmit load balancing' },
  { value: 'balance-alb', label: 'Adaptive load balancing' },
]
const HASH_POLICIES = ['layer2', 'layer2+3', 'layer3+4', 'encap2+3', 'encap3+4']

function blank() {
  return {
    name: '',
    description: '',
    members: [],
    stp: false,
    vlanFiltering: false,
    mode: 'active-backup',
    miiMonitorMs: 100,
    transmitHashPolicy: '',
    primary: '',
    lacpRate: '',
  }
}

const isBond = computed(() => props.kind === 'bond')
const title = computed(() =>
  props.iface ? `${isBond.value ? 'Bond' : 'Bridge'} ${props.iface.name}` : `New ${props.kind}`,
)

/** Links already claimed by another bridge or bond, or carrying addresses. */
const taken = computed(() => {
  const out = new Map()
  for (const i of config.interfaces) {
    if (i.name === form.value.name) continue
    for (const m of i.bridge?.members ?? i.bond?.members ?? []) out.set(m, i.name)
  }
  return out
})

// A bridge may contain a bond, which is how a resilient LAN is built, but
// never another bridge or a tunnel. A bond takes plain links only.
const options = computed(() => {
  const names = new Set()
  for (const c of props.candidates) {
    if (c.name === form.value.name || c.kind === 'bridge' || c.kind === 'wireguard') continue
    if (isBond.value && c.kind === 'bond') continue
    names.add(c.name)
  }
  // A bond that so far exists only in the draft is a perfectly good
  // bridge member, so configured interfaces count too.
  for (const i of config.interfaces) {
    if (i.name === form.value.name || i.vlan || i.wireguard || i.bridge) continue
    if (isBond.value && i.bond) continue
    names.add(i.name)
  }
  return [...names].sort().map((name) => ({
    name,
    takenBy: taken.value.get(name) ?? '',
    zone: config.findInterface(name)?.zone ?? '',
  }))
})

const suggestedName = computed(() => {
  const prefix = isBond.value ? 'bond' : 'br'
  for (let i = 0; i < 100; i++) {
    if (!config.findInterface(`${prefix}${i}`)) return `${prefix}${i}`
  }
  return `${prefix}0`
})

watch(
  () => [open.value, props.iface, props.kind],
  () => {
    if (!open.value) return
    error.value = ''
    const i = props.iface
    if (!i) {
      form.value = { ...blank(), name: suggestedName.value }
      return
    }
    form.value = {
      ...blank(),
      name: i.name,
      description: i.description ?? '',
      members: [...(i.bridge?.members ?? i.bond?.members ?? [])],
      stp: i.bridge?.stp ?? false,
      vlanFiltering: i.bridge?.vlanFiltering ?? false,
      mode: i.bond?.mode ?? 'active-backup',
      miiMonitorMs: i.bond?.miiMonitorMs ?? 100,
      transmitHashPolicy: i.bond?.transmitHashPolicy ?? '',
      primary: i.bond?.primary ?? '',
      lacpRate: i.bond?.lacpRate ?? '',
    }
  },
  { immediate: true },
)

function toggleMember(name) {
  const at = form.value.members.indexOf(name)
  if (at === -1) form.value.members.push(name)
  else form.value.members.splice(at, 1)
  if (!form.value.members.includes(form.value.primary)) form.value.primary = ''
}

function save() {
  error.value = ''
  const f = form.value
  if (!f.members.length) {
    error.value = `A ${props.kind} needs at least one interface.`
    return
  }
  if (!props.iface && config.findInterface(f.name)) {
    error.value = `${f.name} is already configured.`
    return
  }
  const existing = props.iface ? config.findInterface(props.iface.name) : null
  const iface = {
    ...(existing ?? { enabled: true, ipv4: { mode: 'none' }, ipv6: { mode: 'none' } }),
    name: f.name,
    description: f.description,
  }
  delete iface.bridge
  delete iface.bond
  if (isBond.value) {
    iface.bond = { members: [...f.members], mode: f.mode }
    if (Number(f.miiMonitorMs) > 0) iface.bond.miiMonitorMs = Number(f.miiMonitorMs)
    if (f.transmitHashPolicy) iface.bond.transmitHashPolicy = f.transmitHashPolicy
    if (f.primary && f.mode === 'active-backup') iface.bond.primary = f.primary
    if (f.lacpRate && f.mode === '802.3ad') iface.bond.lacpRate = f.lacpRate
  } else {
    iface.bridge = { members: [...f.members], stp: f.stp, vlanFiltering: f.vlanFiltering }
  }
  if (!iface.description) delete iface.description
  // Members become ports: they give up their zone and addressing.
  for (const m of f.members) {
    const member = config.findInterface(m)
    if (!member) continue
    config.upsertInterface({
      ...member,
      zone: '',
      ipv4: { mode: 'none' },
      ipv6: { mode: 'none' },
    })
  }
  config.upsertInterface(iface)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="title"
    :description="
      isBond
        ? 'Its members give up their own addresses.'
        : 'Members share one address and one set of rules.'
    "
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="agg-name" label="Name">
          <input
            id="agg-name"
            v-model="form.name"
            class="input font-mono"
            :disabled="!!iface"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="agg-desc" label="Description">
          <input id="agg-desc" v-model="form.description" class="input" />
        </FormField>
      </div>

      <fieldset class="space-y-2">
        <legend class="subsection-title">Interfaces</legend>
        <p v-if="!options.length" class="text-sm text-neutral-500">
          No interfaces are available to add.
        </p>
        <label
          v-for="o in options"
          :key="o.name"
          class="flex items-center gap-2 text-sm"
          :class="{ 'opacity-50': o.takenBy && !form.members.includes(o.name) }"
        >
          <input
            type="checkbox"
            class="size-4 rounded border-neutral-300"
            :checked="form.members.includes(o.name)"
            :disabled="!!o.takenBy && !form.members.includes(o.name)"
            @change="toggleMember(o.name)"
          />
          <span class="font-mono">{{ o.name }}</span>
          <span v-if="o.takenBy" class="text-xs text-neutral-500">already in {{ o.takenBy }}</span>
          <span v-else-if="o.zone" class="text-sm text-amber-700 dark:text-amber-400">
            in zone {{ o.zone }}, cleared when it becomes a port
          </span>
        </label>
      </fieldset>

      <template v-if="isBond">
        <FormField id="agg-mode" label="Mode">
          <select id="agg-mode" v-model="form.mode" class="input">
            <option v-for="m in BOND_MODES" :key="m.value" :value="m.value">{{ m.label }}</option>
          </select>
        </FormField>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField
            id="agg-mii"
            label="Link check interval (ms)"
            hint="How often members are checked for carrier. 0 turns it off."
          >
            <input
              id="agg-mii"
              v-model.number="form.miiMonitorMs"
              type="number"
              min="0"
              max="10000"
              class="input w-32 font-mono"
            />
          </FormField>
          <FormField
            v-if="form.mode === 'active-backup'"
            id="agg-primary"
            label="Preferred interface"
            hint="Used whenever it is up."
          >
            <select id="agg-primary" v-model="form.primary" class="input">
              <option value="">No preference</option>
              <option v-for="m in form.members" :key="m" :value="m">{{ m }}</option>
            </select>
          </FormField>
          <FormField
            v-if="['802.3ad', 'balance-xor', 'balance-tlb', 'balance-alb'].includes(form.mode)"
            id="agg-hash"
            label="Transmit hash policy"
            hint="Which member a flow takes."
          >
            <select id="agg-hash" v-model="form.transmitHashPolicy" class="input">
              <option value="">Kernel default</option>
              <option v-for="p in HASH_POLICIES" :key="p" :value="p">{{ p }}</option>
            </select>
          </FormField>
          <FormField v-if="form.mode === '802.3ad'" id="agg-lacp" label="LACP rate">
            <select id="agg-lacp" v-model="form.lacpRate" class="input">
              <option value="">Kernel default</option>
              <option value="slow">Slow (every 30s)</option>
              <option value="fast">Fast (every second)</option>
            </select>
          </FormField>
        </div>
      </template>
      <template v-else>
        <div class="flex flex-wrap gap-4 text-sm">
          <label class="flex items-center gap-2">
            <input v-model="form.stp" type="checkbox" class="size-4 rounded border-neutral-300" />
            Spanning tree (guards against loops, delays each port coming up)
          </label>
          <label class="flex items-center gap-2">
            <input
              v-model="form.vlanFiltering"
              type="checkbox"
              class="size-4 rounded border-neutral-300"
            />
            VLAN filtering
          </label>
        </div>
      </template>

      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
