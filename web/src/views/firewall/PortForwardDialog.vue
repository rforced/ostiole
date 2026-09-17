<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { newId } from '@/lib/ids'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'
import { PRIORITY_HINT, TIERS, UNSET_LABEL } from '@/views/firewall/shaping/tiers'

const props = defineProps({ forward: { type: Object, default: null } })
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
    protocol: 'tcp',
    ports: '',
    target: '',
    targetPort: '',
    reflection: false,
    priority: '',
  }
}

watch(
  () => [open.value, props.forward],
  () => {
    if (!open.value) return
    const f = props.forward
    form.value = f
      ? {
          ...blank(),
          ...f,
          ports: f.ports.join(', '),
          targetPort: f.targetPort ?? '',
          priority: f.priority ?? '',
        }
      : blank()
  },
  { immediate: true },
)

function save() {
  const f = form.value
  const out = {
    id: f.id || newId('pf'),
    enabled: f.enabled,
    zone: f.zone,
    protocol: f.protocol,
    ports: parseList(f.ports),
    target: f.target.trim(),
  }
  if (f.description) out.description = f.description
  if (f.targetPort) out.targetPort = String(f.targetPort).trim()
  if (f.reflection) out.reflection = true
  if (f.priority) out.priority = f.priority
  config.upsertPortForward(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="forward ? `Port forward ${forward.id}` : 'New port forward'"
    description="Forwarded traffic is allowed without a rule of its own."
  >
    <form class="space-y-4" @submit.prevent="save">
      <FormField id="pf-desc" label="Description">
        <input id="pf-desc" v-model="form.description" class="input" />
      </FormField>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="pf-zone" label="Arriving in zone">
          <select id="pf-zone" v-model="form.zone" class="input" required>
            <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
          </select>
        </FormField>
        <FormField id="pf-proto" label="Protocol">
          <select id="pf-proto" v-model="form.protocol" class="input">
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
            <option value="tcp+udp">TCP + UDP</option>
          </select>
        </FormField>
        <FormField id="pf-ports" label="Ports" hint="e.g. 443 or 27015-27020">
          <input
            id="pf-ports"
            v-model="form.ports"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="pf-target" label="Target address">
          <input
            id="pf-target"
            v-model="form.target"
            class="input font-mono"
            required
            spellcheck="false"
            placeholder="10.0.0.5"
          />
        </FormField>
        <FormField id="pf-tport" label="Target port" hint="Empty keeps the original port.">
          <input
            id="pf-tport"
            v-model="form.targetPort"
            class="input w-32 font-mono"
            spellcheck="false"
          />
        </FormField>
        <FormField
          v-if="config.shapedInterfaces.length"
          id="pf-priority"
          label="Priority"
          :hint="PRIORITY_HINT"
        >
          <select id="pf-priority" v-model="form.priority" class="input">
            <option value="">{{ UNSET_LABEL }}</option>
            <option v-for="t in TIERS" :key="t.value" :value="t.value">
              {{ t.label }} — {{ t.hint }}
            </option>
          </select>
        </FormField>
      </div>
      <label class="flex items-start gap-2 text-sm">
        <input
          v-model="form.reflection"
          type="checkbox"
          class="mt-0.5 size-4 rounded border-neutral-300"
        />
        <span
          ><span class="font-medium">NAT reflection</span>: inside hosts reach the target by the
          outside address too.</span
        >
      </label>
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
