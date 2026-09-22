<script setup>
import { computed, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
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
// These servers are the router's own resolvers only while the DNS service is
// off. With it on the router asks dnsmasq like any client, and this field is
// at most the fallback the forwarder uses when it has no upstreams of its own.
const dnsHint = computed(() => {
  const svc = config.draft.services?.dns
  if (!svc?.enabled) return 'Comma separated. This router resolves names here.'
  if ((svc.resolver || 'forward') === 'forward' && !(svc.upstreams ?? []).length)
    return 'Comma separated. The DNS service forwards here until it has upstreams of its own.'
  return 'Comma separated. Unused while the DNS service answers for this router.'
})

// Bounds and cost mirror model.FirewallLog; the ring grows into its ceiling,
// so the figure quoted is what a full log costs, not what it costs today.
const DEFAULT_ENTRIES = 20000
const MAX_ENTRIES = 1000000
const ENTRY_BYTES = 350

/** Empty means the default, which the hint names. */
const fwLogEntries = computed({
  get: () => management.value.firewallLog?.entries || '',
  set: (v) => {
    if (Number.isFinite(v) && v > 0) management.value.firewallLog = { entries: v }
    else delete management.value.firewallLog
  },
})
const entriesMB = computed(() =>
  Math.round(
    ((fwLogEntries.value || DEFAULT_ENTRIES) * ENTRY_BYTES) / (1024 * 1024),
  ).toLocaleString(),
)
</script>

<template>
  <SectionCard title="System settings">
    <div class="space-y-4">
      <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
        <FormField id="sys-hostname" label="Hostname">
          <input id="sys-hostname" v-model="system.hostname" class="input" spellcheck="false" />
        </FormField>
        <FormField id="sys-timezone" label="Timezone" :hint="zoneHint">
          <select id="sys-timezone" v-model="zone" class="input">
            <option v-for="z in zones" :key="z" :value="z">{{ z }}</option>
          </select>
        </FormField>
        <FormField id="sys-dns" label="DNS servers for this router" :hint="dnsHint">
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
      <ToggleRow
        v-model="management.sshPasswords"
        label="Allow password logins over SSH"
        hint="Unchecked, only keys get in. Nothing checks that you have one."
      />
      <ToggleRow
        v-model="management.logDefaultDrops"
        label="Log dropped packets"
        hint="The default for every interface, covering the drops this firewall makes on its own.
          Any one interface can say otherwise under Interfaces, and any zone can under Zones."
      />
      <FormField
        id="sys-fwlog"
        label="Firewall log entries"
        :hint="`${DEFAULT_ENTRIES.toLocaleString()} is the default. About ${entriesMB} MB of memory
          when full; the log is kept in memory only.`"
      >
        <input
          id="sys-fwlog"
          v-model.number="fwLogEntries"
          type="number"
          min="0"
          :max="MAX_ENTRIES"
          class="input w-40"
        />
      </FormField>
    </div>
  </SectionCard>
</template>
