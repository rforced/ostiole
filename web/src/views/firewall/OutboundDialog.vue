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
  return { id: '', description: '', enabled: true, zone: external, source: '' }
}

watch(
  () => [open.value, props.rule],
  () => {
    if (!open.value) return
    const r = props.rule
    form.value = r ? { ...blank(), ...r, source: joinList(r.source) } : blank()
  },
  { immediate: true },
)

function save() {
  const f = form.value
  const out = { id: f.id || newId('nat'), enabled: f.enabled, zone: f.zone }
  if (f.description) out.description = f.description
  const source = parseList(f.source)
  if (source.length) out.source = source
  config.upsertOutboundRule(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="rule ? `Outbound NAT ${rule.id}` : 'New outbound NAT rule'"
    description="Masquerades traffic leaving the zone, optionally only from the listed sources."
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
      <FormField
        id="nat-source"
        label="Source networks"
        hint="One per line. Empty means all IPv4 traffic."
      >
        <textarea
          id="nat-source"
          v-model="form.source"
          class="input h-24 font-mono"
          spellcheck="false"
        ></textarea>
      </FormField>
      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
        Enabled
      </label>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
