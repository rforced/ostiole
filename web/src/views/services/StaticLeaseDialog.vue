<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ lease: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })
const config = useConfigStore()
const form = ref({ mac: '', ip: '', hostname: '', description: '' })

watch(
  () => [open.value, props.lease],
  () => {
    if (open.value)
      form.value = { mac: '', ip: '', hostname: '', description: '', ...(props.lease ?? {}) }
  },
  { immediate: true },
)

function save() {
  const out = { mac: form.value.mac.trim().toLowerCase(), ip: form.value.ip.trim() }
  if (form.value.hostname) out.hostname = form.value.hostname.trim()
  if (form.value.description) out.description = form.value.description
  config.upsertStaticLease(out, props.lease?.mac ?? out.mac)
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="lease ? `Static lease ${lease.mac}` : 'New static lease'">
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="sl-mac" label="MAC address">
          <input
            id="sl-mac"
            v-model="form.mac"
            class="input font-mono"
            placeholder="aa:bb:cc:dd:ee:ff"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="sl-ip" label="IPv4 address">
          <input id="sl-ip" v-model="form.ip" class="input font-mono" required spellcheck="false" />
        </FormField>
        <FormField id="sl-host" label="Hostname" hint="Also resolvable by the DNS service.">
          <input id="sl-host" v-model="form.hostname" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField id="sl-desc" label="Description">
          <input id="sl-desc" v-model="form.description" class="input" />
        </FormField>
      </div>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
