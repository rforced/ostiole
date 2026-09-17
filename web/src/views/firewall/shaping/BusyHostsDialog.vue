<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'
import { TIERS } from '@/views/firewall/shaping/tiers'

/** What the router will accept, and what it offers when asked first. */
const MIN = 10
const MAX = 100000
const DEFAULT_CONNECTIONS = 200

const props = defineProps({
  /** The zone being edited, or null for a new one. */
  zone: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return { zone: '', connections: String(DEFAULT_CONNECTIONS), priority: 'bulk' }
}

/**
 * Zones a tally can go on: the count is per source address, so it only
 * says anything where the sources are this network's own hosts. A zone
 * that already has one is edited rather than added again.
 */
const candidates = computed(() =>
  config.zones.filter((z) => !z.external && (props.zone?.name === z.name || !z.busy)),
)

watch(
  () => [open.value, props.zone],
  () => {
    if (!open.value) return
    const z = props.zone
    if (!z) {
      form.value = blank()
      if (candidates.value.length === 1) form.value.zone = candidates.value[0].name
      return
    }
    form.value = {
      zone: z.name,
      connections: String(z.busy?.connections ?? DEFAULT_CONNECTIONS),
      priority: z.busy?.priority ?? 'bulk',
    }
  },
  { immediate: true },
)

const connections = computed(() => Number(form.value.connections))
const valid = computed(
  () =>
    Boolean(form.value.zone) &&
    Boolean(form.value.priority) &&
    Number.isInteger(connections.value) &&
    connections.value >= MIN &&
    connections.value <= MAX,
)

function save() {
  if (!valid.value) return
  config.setBusy(form.value.zone, {
    connections: connections.value,
    priority: form.value.priority,
  })
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="zone ? `Busy hosts on ${zone.name}` : 'Hold back busy hosts'"
    description="A device with a lot of connections open at once is usually sharing files, whatever port it is doing it on. Its connections past the limit go in a lower priority until it settles down."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField
        id="busy-zone"
        label="Zone"
        hint="The count is per device, so it belongs on a zone whose devices are yours."
      >
        <select id="busy-zone" v-model="form.zone" class="input" required :disabled="!!zone">
          <option value="" disabled>Choose</option>
          <option v-for="z in candidates" :key="z.name" :value="z.name">{{ z.name }}</option>
        </select>
      </FormField>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField
          id="busy-count"
          label="Connections"
          hint="200 is comfortable for a busy workstation. Ordinary browsing rarely passes it."
        >
          <input
            id="busy-count"
            v-model="form.connections"
            class="input w-32 font-mono tabular-nums"
            type="number"
            :min="MIN"
            :max="MAX"
            step="1"
            inputmode="numeric"
            required
          />
        </FormField>
        <FormField
          id="busy-priority"
          label="Priority"
          hint="Where the connections past the limit go. The ones already open keep what they had."
        >
          <select id="busy-priority" v-model="form.priority" class="input" required>
            <option v-for="t in TIERS" :key="t.value" :value="t.value">
              {{ t.label }} — {{ t.hint }}
            </option>
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
