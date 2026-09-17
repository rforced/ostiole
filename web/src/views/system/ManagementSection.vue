<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
// Older drafts may lack the management block; create it once, outside any computed.
if (!config.draft.system.management) config.draft.system.management = { webPort: 443, sshPort: 22 }
const system = computed(() => config.draft.system)
const management = computed(() => system.value.management)
const dns = computed({
  get: () => (system.value.dnsServers ?? []).join(', '),
  set: (v) => {
    const list = parseList(v)
    if (list.length) system.value.dnsServers = list
    else delete system.value.dnsServers
  },
})
</script>

<template>
  <section class="card space-y-4" aria-labelledby="mgmt-title">
    <h2 id="mgmt-title" class="card-title">System settings</h2>
    <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
      <FormField id="sys-hostname" label="Hostname">
        <input id="sys-hostname" v-model="system.hostname" class="input" spellcheck="false" />
      </FormField>
      <FormField id="sys-dns" label="DNS servers for this box" hint="Comma separated.">
        <input id="sys-dns" v-model="dns" class="input font-mono" spellcheck="false" />
      </FormField>
      <FormField
        id="sys-web"
        label="Web UI port"
        hint="Kept open from anti-lockout zones. 0 disables that entry."
      >
        <input
          id="sys-web"
          v-model.number="management.webPort"
          type="number"
          min="0"
          max="65535"
          class="input w-32"
        />
      </FormField>
      <FormField id="sys-ssh" label="SSH port" hint="Same anti-lockout treatment.">
        <input
          id="sys-ssh"
          v-model.number="management.sshPort"
          type="number"
          min="0"
          max="65535"
          class="input w-32"
        />
      </FormField>
    </div>
    <label class="flex items-start gap-2 text-sm">
      <input
        v-model="management.logDefaultDrops"
        type="checkbox"
        class="mt-0.5 size-4 rounded border-neutral-300"
      />
      <span>
        Log packets dropped by the default policy
        <span class="block text-neutral-500">
          The default for every interface. Any one of them can say otherwise under
          <RouterLink to="/interfaces" class="underline">Interfaces</RouterLink>.
        </span>
      </span>
    </label>
  </section>
</template>
