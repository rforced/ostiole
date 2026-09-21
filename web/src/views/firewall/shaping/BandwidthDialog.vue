<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

/**
 * Line types, with the labels an operator would use for what is on the
 * wall rather than the names the queue discipline uses for them.
 */
const LINKS = [
  { value: 'ethernet', label: 'Ethernet or fibre' },
  { value: 'docsis', label: 'Cable' },
  { value: 'pppoe-ptm', label: 'VDSL with PPPoE' },
  { value: 'bridged-ptm', label: 'VDSL' },
  { value: 'pppoe-vcmux', label: 'ADSL with PPPoE' },
  { value: 'conservative', label: 'Not sure' },
]

/** Units, largest last so the multiplier is obvious. */
const UNITS = [
  { value: 1000, label: 'kbit/s' },
  { value: 1e6, label: 'Mbit/s' },
  { value: 1e9, label: 'Gbit/s' },
]

const props = defineProps({
  /** The interface being edited, or null for a new one. */
  iface: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return {
    interface: '',
    download: '',
    downloadUnit: 1e6,
    upload: '',
    uploadUnit: 1e6,
    link: 'ethernet',
  }
}

/**
 * Splits a stored bit/s figure into the largest whole unit it fits, so a
 * line entered as 20 Mbit/s comes back as 20 Mbit/s rather than 20000
 * kbit/s.
 */
function split(bits) {
  if (!bits) return { value: '', unit: 1e6 }
  for (const u of [...UNITS].reverse()) {
    if (bits >= u.value && bits % u.value === 0)
      return { value: String(bits / u.value), unit: u.value }
  }
  return { value: String(bits / 1000), unit: 1000 }
}

/**
 * Interfaces a speed can go on: enabled, not the loopback, not a port on
 * something else, and not already listed unless this is that one.
 */
const candidates = computed(() => {
  const members = new Set()
  for (const i of config.interfaces) {
    for (const m of i.bridge?.members ?? i.bond?.members ?? []) members.add(m)
    if (i.pppoe?.parent) members.add(i.pppoe.parent)
  }
  return config.interfaces.filter(
    (i) =>
      i.enabled &&
      i.name !== 'lo' &&
      !members.has(i.name) &&
      (props.iface?.name === i.name || !i.shaping),
  )
})

watch(
  () => [open.value, props.iface],
  () => {
    if (!open.value) return
    const i = props.iface
    if (!i) {
      form.value = blank()
      if (candidates.value.length === 1) form.value.interface = candidates.value[0].name
      return
    }
    const down = split(i.shaping?.download)
    const up = split(i.shaping?.upload)
    form.value = {
      interface: i.name,
      download: down.value,
      downloadUnit: down.unit,
      upload: up.value,
      uploadUnit: up.unit,
      link: i.shaping?.link || 'ethernet',
    }
  },
  { immediate: true },
)

/** The zone the chosen interface is in, which decides how the hints read. */
const internal = computed(() => {
  const iface = config.findInterface(form.value.interface)
  const zone = config.zones.find((z) => z.name === iface?.zone)
  return Boolean(zone) && !zone.external
})

const downloadHint = computed(() =>
  internal.value
    ? 'A cap for the hosts behind this interface.'
    : 'A little under what the line really delivers. Too high and the shaper has nothing to do.',
)

function bits(value, unit) {
  const n = Number(value)
  if (!Number.isFinite(n) || n <= 0) return 0
  return Math.round(n * unit)
}

const download = computed(() => bits(form.value.download, form.value.downloadUnit))
const upload = computed(() => bits(form.value.upload, form.value.uploadUnit))
const valid = computed(
  () => Boolean(form.value.interface) && (download.value > 0 || upload.value > 0),
)

function save() {
  if (!valid.value) return
  const out = {}
  if (download.value) out.download = download.value
  if (upload.value) out.upload = upload.value
  if (form.value.link && form.value.link !== 'ethernet') out.link = form.value.link
  config.setShaping(form.value.interface, out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="iface ? `Speed of ${iface.name}` : 'Set a speed'"
    description="Give the speed the line really reaches. The router then holds the queue here instead of leaving it in the equipment at the other end."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="bw-if" label="Interface">
        <select id="bw-if" v-model="form.interface" class="input" required :disabled="!!iface">
          <option value="" disabled>Choose</option>
          <option v-for="i in candidates" :key="i.name" :value="i.name">
            {{ i.name }}{{ i.zone ? ` (${i.zone})` : '' }}
          </option>
        </select>
      </FormField>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="bw-down" label="Download" :hint="downloadHint">
          <div class="flex gap-2">
            <input
              id="bw-down"
              v-model="form.download"
              class="input w-28 font-mono tabular-nums"
              type="number"
              min="0"
              step="any"
              inputmode="decimal"
            />
            <select
              v-model.number="form.downloadUnit"
              class="input w-28"
              aria-label="Download unit"
            >
              <option v-for="u in UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
            </select>
          </div>
        </FormField>
        <FormField id="bw-up" label="Upload" hint="Leave a direction empty to leave it alone.">
          <div class="flex gap-2">
            <input
              id="bw-up"
              v-model="form.upload"
              class="input w-28 font-mono tabular-nums"
              type="number"
              min="0"
              step="any"
              inputmode="decimal"
            />
            <select v-model.number="form.uploadUnit" class="input w-28" aria-label="Upload unit">
              <option v-for="u in UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
            </select>
          </div>
        </FormField>
        <FormField
          id="bw-link"
          label="Link type"
          hint="Not sure costs a little speed and never under-counts."
        >
          <select id="bw-link" v-model="form.link" class="input">
            <option v-for="l in LINKS" :key="l.value" :value="l.value">{{ l.label }}</option>
          </select>
        </FormField>
      </div>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!valid">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
