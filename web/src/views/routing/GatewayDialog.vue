<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ gateway: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return {
    name: '',
    description: '',
    enabled: true,
    interface: '',
    address: '',
    monitor: '',
    priority: 0,
  }
}

watch(
  () => [open.value, props.gateway],
  () => {
    if (!open.value) return
    form.value = props.gateway ? { ...blank(), ...props.gateway } : blank()
    if (!props.gateway) {
      const wan = config.interfaces.find((i) => {
        const zone = config.zones.find((z) => z.name === i.zone)
        return zone?.external
      })
      if (wan) form.value.interface = wan.name
      form.value.priority = config.gateways.length
    }
  },
  { immediate: true },
)

function save() {
  const f = form.value
  const out = {
    name: f.name.trim(),
    enabled: f.enabled,
    interface: f.interface,
    priority: Number(f.priority) || 0,
  }
  if (f.description) out.description = f.description
  if (f.address) out.address = f.address.trim()
  if (f.monitor) out.monitor = f.monitor.trim()
  config.upsertGateway(out, props.gateway?.name ?? out.name)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="gateway ? `Gateway ${gateway.name}` : 'New gateway'"
    description="The lowest priority that answers its monitor carries the default route. The others wait."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="gw-name" label="Name" hint="Lower case, e.g. wan1.">
          <input
            id="gw-name"
            v-model="form.name"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="gw-desc" label="Description">
          <input id="gw-desc" v-model="form.description" class="input" />
        </FormField>
        <FormField id="gw-if" label="Interface">
          <select id="gw-if" v-model="form.interface" class="input" required>
            <option value="" disabled>Choose</option>
            <option v-for="i in config.interfaces" :key="i.name" :value="i.name">
              {{ i.name }}
            </option>
          </select>
        </FormField>
        <FormField
          id="gw-addr"
          label="Gateway address"
          hint="Empty follows DHCP or a router advertisement."
        >
          <input id="gw-addr" v-model="form.address" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField
          id="gw-monitor"
          label="Monitor address"
          hint="Probed to decide if the line works. Empty pings the gateway itself, which only proves the first hop."
        >
          <input
            id="gw-monitor"
            v-model="form.monitor"
            class="input font-mono"
            spellcheck="false"
            placeholder="9.9.9.9"
          />
        </FormField>
        <FormField id="gw-prio" label="Priority" hint="Lowest wins; equal priorities share.">
          <input
            id="gw-prio"
            v-model="form.priority"
            type="number"
            min="0"
            max="255"
            class="input w-32 font-mono"
          />
        </FormField>
      </div>
      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
        Enabled
      </label>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.interface">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
