<script setup>
import { computed, onMounted, ref, watch } from 'vue'

import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import LogRetention from '@/components/LogRetention.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes, formatCount } from '@/lib/format'
import { SETTLE_MS, heldLine } from '@/lib/log'
import { DESTINATION_WINDOWS, deviceLabel, serviceLabel, useTrafficStream } from '@/lib/traffic'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()

const traffic = computed(() => config.draft?.traffic ?? {})
const devicesOn = computed(() => Boolean(traffic.value.devices))
const destinations = computed(() => traffic.value.destinations ?? {})
/** Out of the draft while every value is the default. */
function setDestinations(d) {
  const t = { ...traffic.value }
  if (Object.keys(d).length) t.destinations = d
  else delete t.destinations
  if (Object.keys(t).length) config.draft.traffic = t
  else delete config.draft.traffic
}
const enabled = computed({
  get: () => Boolean(destinations.value.enabled),
  set: (on) => {
    const d = { ...destinations.value }
    if (on) d.enabled = true
    else delete d.enabled
    setDestinations(d)
  },
})
/** Entries and days, beside the switch. */
const retention = computed({
  get: () => {
    const rest = { ...destinations.value }
    delete rest.enabled
    return rest
  },
  set: (v) => setDestinations(destinations.value.enabled ? { enabled: true, ...v } : { ...v }),
})

const win = ref('24h')
const device = ref('')
const query = ref('')
const live = ref(true)
/** The last first page: {entries, more, next, held, oldest, enabled}. */
const page = ref(null)
const rows = ref([])
/** The devices the filter offers. */
const devices = ref([])

function params(offset = 0) {
  const p = { window: win.value }
  if (device.value) p.device = device.value
  if (query.value.trim()) p.q = query.value.trim()
  if (offset) p.offset = offset
  return p
}

const read = useAsync(async () => {
  const p = await api.traffic.destinations(params())
  page.value = p
  rows.value = p.entries ?? []
})
const more = useAsync(async () => {
  if (!page.value?.more) return
  // Reading past the first page is reading history, so Live stops.
  live.value = false
  const p = await api.traffic.destinations(params(page.value.next))
  page.value = { ...page.value, more: p.more, next: p.next }
  rows.value = [...rows.value, ...(p.entries ?? [])]
})
const readDevices = useAsync(async () => {
  devices.value = (await api.traffic.devices('24h')).devices ?? []
})
onMounted(() => {
  read.run()
  readDevices.run()
})
watch([win, device], () => read.run())
let settle = 0
watch(query, () => {
  clearTimeout(settle)
  settle = setTimeout(() => read.run(), SETTLE_MS)
})
watch(
  () => config.saved?.traffic?.destinations?.enabled,
  () => read.run(),
)

// Each read of the connection table moves the counts: read the first page
// again while Live is on.
const stream = useTrafficStream({
  devices: () => {
    if (live.value) read.run()
  },
  reopened: () => read.run(),
})
watch(live, (on) => {
  if (on) read.run()
})

const running = computed(() => Boolean(page.value?.enabled))
const error = computed(
  () => read.error.value || more.error.value || stream.error.value || readDevices.error.value,
)
const empty = computed(() => {
  if (!page.value) return 'Reading…'
  if (query.value.trim()) return `Nothing matches "${query.value.trim()}".`
  return 'No destinations.'
})
</script>

<template>
  <div class="space-y-5">
    <LogRetention v-model="retention" log="destinations" title="Recording" :off="!enabled">
      <template #actions>
        <ToggleRow
          id="destinations-enabled"
          v-model="enabled"
          variant="switch"
          label="Record destinations"
          :hint="devicesOn ? '' : 'Needs counting per device.'"
          :disabled="auth.readOnly || (!devicesOn && !enabled)"
        />
      </template>
    </LogRetention>

    <template v-if="running">
      <div class="flex flex-wrap items-center gap-3">
        <select v-model="win" class="input w-36 max-sm:w-full" aria-label="Window">
          <option v-for="w in DESTINATION_WINDOWS" :key="w.value" :value="w.value">
            {{ w.label }}
          </option>
        </select>
        <select v-model="device" class="input w-56 max-sm:w-full" aria-label="Device">
          <option value="">All devices</option>
          <option v-for="d in devices" :key="d.id" :value="d.id">{{ deviceLabel(d) }}</option>
        </select>
        <LiveButton v-model="live" :failing="Boolean(error)" />
      </div>

      <SectionCard title="Destinations" flush>
        <template #intro>
          {{ page ? heldLine(page.held, page.oldest) : '' }}
        </template>
        <div class="card-strip flex flex-wrap items-center gap-3">
          <SearchBox v-model="query" placeholder="name, address, service, or device" />
          <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
        </div>
        <!-- On a phone a destination is two lines: what, and how much. -->
        <table class="table table-flow">
          <thead>
            <tr>
              <th>Destination</th>
              <th v-if="!device">Device</th>
              <th>Service</th>
              <th class="text-right">Down</th>
              <th class="text-right">Up</th>
              <th class="text-right">Connections</th>
              <th>Last seen</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!rows.length">
              <td :colspan="device ? 6 : 7" class="text-ink-muted">{{ empty }}</td>
            </tr>
            <tr
              v-for="r in rows"
              :key="`${r.device} ${r.destination} ${r.protocol} ${r.port}`"
              class="max-sm:before:order-4 max-sm:before:basis-full max-sm:before:content-['']"
            >
              <td class="max-w-72 max-sm:order-1 max-sm:max-w-none max-sm:basis-full">
                <div class="truncate font-medium">{{ r.destination }}</div>
                <div
                  v-if="r.address && r.address !== r.destination"
                  class="font-mono text-xs text-ink-muted"
                >
                  {{ r.address }}
                </div>
              </td>
              <td v-if="!device" class="max-sm:order-2">
                {{ r.device === 'router' ? 'This router' : r.deviceName || r.device }}
              </td>
              <td class="max-sm:order-3 max-sm:text-ink-muted">{{ serviceLabel(r) }}</td>
              <td
                class="text-right tabular-nums max-sm:order-5 max-sm:before:mr-1 max-sm:before:content-['↓']"
              >
                {{ formatBytes(r.down) }}
              </td>
              <td
                class="text-right tabular-nums max-sm:order-6 max-sm:before:mr-1 max-sm:before:content-['↑']"
              >
                {{ formatBytes(r.up) }}
              </td>
              <td class="text-right tabular-nums max-sm:order-7">
                {{ formatCount(r.connections) }}
              </td>
              <td class="text-xs whitespace-nowrap max-sm:order-8">
                {{ new Date(r.lastSeen).toLocaleString() }}
              </td>
            </tr>
          </tbody>
        </table>
        <LoadMore
          :more="Boolean(page?.more)"
          :busy="more.busy.value"
          :rows="rows.length"
          @load="more.run()"
        />
      </SectionCard>
    </template>
  </div>
</template>
