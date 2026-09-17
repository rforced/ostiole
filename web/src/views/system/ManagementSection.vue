<script setup>
import { computed, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
// Older drafts may lack the management block; create it once, outside any computed.
if (!config.draft.system.management) config.draft.system.management = { webPort: 443, sshPort: 22 }
const system = computed(() => config.draft.system)
const management = computed(() => system.value.management)

/** The zones the router offers and the one its clock reads now. */
const clock = ref({ zones: ['UTC'], current: 'UTC' })
const loadClock = useAsync(async () => {
  clock.value = await api.timezones()
})
onMounted(loadClock.run)

// A configuration that says nothing runs in UTC, so that is what the
// picker shows.
const zone = computed({
  get: () => system.value.timezone || 'UTC',
  set: (v) => {
    system.value.timezone = v
  },
})
// A zone the router no longer offers stays on the list rather than being
// swapped for its neighbour behind the operator's back.
const zones = computed(() => {
  const list = clock.value.zones?.length ? clock.value.zones : ['UTC']
  return list.includes(zone.value) ? list : [zone.value, ...list]
})
const zoneHint = computed(() =>
  clock.value.current && clock.value.current !== zone.value
    ? `The clock reads ${clock.value.current} until you apply.`
    : 'Log entries and schedules are read in this zone.',
)
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
      <FormField id="sys-timezone" label="Timezone" :hint="zoneHint">
        <select id="sys-timezone" v-model="zone" class="input">
          <option v-for="z in zones" :key="z" :value="z">{{ z }}</option>
        </select>
      </FormField>
      <FormField id="sys-dns" label="DNS servers for this router" hint="Comma separated.">
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
