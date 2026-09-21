<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { newId } from '@/lib/ids'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ rule: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  const external = config.zones.find((z) => z.external)?.name ?? config.zones[0]?.name ?? ''
  return {
    id: '',
    description: '',
    enabled: true,
    zone: external,
    source: '',
    destination: '',
    address: '',
    noNat: false,
  }
}

watch(
  () => [open.value, props.rule],
  () => {
    if (!open.value) return
    const r = props.rule
    form.value = r
      ? {
          ...blank(),
          ...r,
          source: joinList(r.source),
          destination: joinList(r.destination),
          address: r.address ?? '',
          noNat: r.noNat ?? false,
        }
      : blank()
  },
  { immediate: true },
)

function save() {
  const f = form.value
  const out = { id: f.id || newId('nat'), enabled: f.enabled, zone: f.zone }
  if (f.description) out.description = f.description
  const source = parseList(f.source)
  if (source.length) out.source = source
  const destination = parseList(f.destination)
  if (destination.length) out.destination = destination
  if (f.noNat) out.noNat = true
  else if (f.address.trim()) out.address = f.address.trim()
  config.upsertOutboundRule(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="rule ? `Outbound NAT ${rule.id}` : 'Add outbound rule'"
    description="With nothing else set it masquerades everything leaving the zone."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="nat-desc" label="Description">
        <input id="nat-desc" v-model="form.description" class="input" />
      </FormField>
      <FormField id="nat-zone" label="Leaving via zone">
        <select id="nat-zone" v-model="form.zone" class="input" required>
          <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
        </select>
      </FormField>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField
          id="nat-source"
          label="Source networks"
          hint="One per line. Empty matches anything."
        >
          <textarea
            id="nat-source"
            v-model="form.source"
            class="input h-24 font-mono"
            spellcheck="false"
          ></textarea>
        </FormField>
        <FormField
          id="nat-dest"
          label="Destination networks"
          hint="One per line. Empty matches anywhere."
        >
          <textarea
            id="nat-dest"
            v-model="form.destination"
            class="input h-24 font-mono"
            spellcheck="false"
          ></textarea>
        </FormField>
      </div>
      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.noNat" type="checkbox" class="size-4 rounded border-line-2" />
        Do not translate this traffic
      </label>
      <FormField
        v-if="!form.noNat"
        id="nat-address"
        label="Leave as"
        hint="An address this firewall answers to. Empty uses the interface address."
      >
        <input
          id="nat-address"
          v-model="form.address"
          class="input font-mono"
          placeholder="the interface address"
          spellcheck="false"
        />
      </FormField>
      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-line-2" />
        Enabled
      </label>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
