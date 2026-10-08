<script setup>
import { computed, ref, watch } from 'vue'

import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import RandomMacBadge from '@/components/RandomMacBadge.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import { useSearch } from '@/lib/search'
import { byAddress, byText, byTime, useSort } from '@/lib/sort'
import { useWake, wakeInterfaces } from '@/lib/wol'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import StaticLeaseDialog from '@/views/services/dhcp/StaticLeaseDialog.vue'

/** How often Live reads the leases again. */
const LIVE_MS = 2000

const COLUMNS = [
  ['address', 'Address'],
  ['client', 'Client'],
  ['hostname', 'Hostname'],
  ['interface', 'Interface'],
  ['seen', 'Last seen'],
  ['renewed', 'Last renewed'],
  ['expires', 'Expires'],
]

const auth = useAuthStore()
const config = useConfigStore()
const leases = ref([])
const live = ref(true)
const open = ref(false)
const editing = ref(null)
const prefill = ref(null)
const waking = useWake()

/**
 * The server names a lease's interface from the configuration it is
 * running, and sends a wake the same way, so this reads the saved one.
 */
const wakeable = computed(
  () =>
    new Set(
      wakeInterfaces(config.saved)
        .filter((i) => i.enabled)
        .map((i) => i.name),
    ),
)
const canWake = (l) => Boolean(l.mac && l.family !== 6 && wakeable.value.has(l.interface))

/** Pins a client to the address it has. One already pinned in the draft opens as it is. */
function makeStatic(l) {
  const mac = l.mac.toLowerCase()
  editing.value =
    (config.draft?.services?.dhcp?.staticLeases ?? []).find((s) => s.mac.toLowerCase() === mac) ??
    null
  prefill.value = { mac, ip: l.ip, hostname: l.hostname ?? '' }
  open.value = true
}

const load = useAsync(
  async () => {
    leases.value = await api.services.leases()
  },
  // Live owns the poll, and starts on.
  { interval: LIVE_MS, immediate: true },
)
watch(live, (on) => {
  if (on) {
    load.run()
    load.start()
  } else {
    load.stop()
  }
})

const { query, shown } = useSearch(leases, (l) => ({
  values: [l.ip, l.clientId, l.hostname, l.interface, l.description],
  macs: [l.mac],
}))

/** Last renewed, newest first, shows leases as they are handed out. */
const sort = useSort(
  shown,
  {
    address: byAddress((l) => l.ip),
    client: byText((l) => l.mac || l.clientId),
    hostname: byText((l) => l.hostname),
    interface: byText((l) => l.interface),
    seen: byTime((l) => l.seen),
    renewed: byTime((l) => l.renewed),
    expires: byTime((l) => l.expires, 'asc'),
  },
  { by: 'address', tie: 'address' },
)
const rows = sort.sorted

/** A time is worth showing for a client that is not there to speak for itself. */
const seen = (l) => (l.online ? '—' : formatWhen(l.seen))

const empty = computed(() =>
  emptyText(load, leases.value.length ? `Nothing matches "${query.value.trim()}".` : 'No leases.'),
)
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Leases" :count="leases.length" flush>
      <template #actions>
        <SortSelect :sort="sort" :columns="COLUMNS" />
        <LiveButton v-model="live" :failing="Boolean(load.error.value)" />
      </template>
      <div class="card-strip-row">
        <SearchBox
          v-model="query"
          placeholder="address, MAC, hostname, or interface"
          :shown="shown.length"
          :total="leases.length"
        />
      </div>
      <div v-if="load.error.value" class="card-strip">
        <ErrorLine>{{ load.error.value }}</ErrorLine>
      </div>
      <div v-if="waking.errors.value.length" class="card-strip">
        <ErrorLine>{{ waking.errors.value[0] }}</ErrorLine>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <SortHeader by="address" :sort="sort">Address</SortHeader>
            <SortHeader by="client" :sort="sort">Client</SortHeader>
            <SortHeader by="hostname" :sort="sort">Hostname</SortHeader>
            <SortHeader by="interface" :sort="sort">Interface</SortHeader>
            <SortHeader by="seen" :sort="sort">Last seen</SortHeader>
            <SortHeader by="renewed" :sort="sort">Last renewed</SortHeader>
            <SortHeader by="expires" :sort="sort">Expires</SortHeader>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="8" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr v-for="l in rows" :key="l.ip + (l.mac || l.clientId)">
            <td data-label="Address">
              <div class="font-mono text-code">{{ l.ip }}</div>
              <div class="whitespace-nowrap">
                <span class="badge" :class="{ 'badge-ok': l.online }">
                  {{ l.online ? 'online' : 'offline' }}
                </span>
                <span v-if="l.static" class="badge ml-1">static</span>
                <span v-if="l.family === 6" class="badge ml-1">v6</span>
              </div>
            </td>
            <!-- DHCPv6 identifies clients by DUID, so there is no MAC. A
                 DUID runs long, so it wraps rather than widen the table. -->
            <td class="max-w-48 max-sm:max-w-none" data-label="Client">
              <span class="font-mono text-code break-all">{{ l.mac || l.clientId || '—' }}</span>
              <RandomMacBadge :mac="l.mac" />
            </td>
            <td data-label="Hostname">
              <div class="font-mono text-code whitespace-nowrap">{{ l.hostname || '—' }}</div>
              <div v-if="l.description" class="text-ink-muted">{{ l.description }}</div>
            </td>
            <td class="font-mono text-code" data-label="Interface">{{ l.interface || '—' }}</td>
            <td class="when" data-label="Last seen">
              {{ seen(l) }}
            </td>
            <td class="when" data-label="Last renewed">
              {{ formatWhen(l.renewed) }}
            </td>
            <td class="when" data-label="Expires">
              {{ formatWhen(l.expires, 'never') }}
            </td>
            <td class="actions" data-label="">
              <button
                v-if="canWake(l) && !auth.readOnly"
                type="button"
                class="link-action"
                :disabled="waking.busy.value !== ''"
                :aria-busy="waking.busy.value === l.mac"
                @click="waking.wake(l, l.hostname || l.mac)"
              >
                Wake
              </button>
              <button
                v-if="l.mac && !l.static && l.family !== 6 && !auth.readOnly"
                type="button"
                class="link-action"
                :class="{ 'ml-3': canWake(l) }"
                @click="makeStatic(l)"
              >
                Make static
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <StaticLeaseDialog v-model:open="open" :lease="editing" :prefill="prefill" />
  </div>
</template>
