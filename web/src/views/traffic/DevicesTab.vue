<script setup>
import { computed, onMounted, ref, watch } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import LiveButton from '@/components/LiveButton.vue'
import RandomMacBadge from '@/components/RandomMacBadge.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes, formatRate } from '@/lib/format'
import { useSearch } from '@/lib/search'
import { byNumber, byText, byTime, useSort } from '@/lib/sort'
import { WINDOWS, deviceLabel, sinceLine, useTrafficStream } from '@/lib/traffic'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import DeviceDialog from '@/views/traffic/DeviceDialog.vue'

const COLUMNS = [
  ['device', 'Device'],
  ['down', 'Down'],
  ['up', 'Up'],
  ['seen', 'Last seen'],
]

const auth = useAuthStore()
const config = useConfigStore()

/**
 * The switch, kept out of the draft while off. Destinations belong to
 * devices, so switching this off switches them off with it.
 */
const counting = computed({
  get: () => Boolean(config.draft?.traffic?.devices),
  set: (on) => {
    const t = { ...(config.draft.traffic ?? {}) }
    if (on) t.devices = true
    else {
      delete t.devices
      if (t.destinations?.enabled) {
        const d = { ...t.destinations }
        delete d.enabled
        if (Object.keys(d).length) t.destinations = d
        else delete t.destinations
      }
    }
    if (Object.keys(t).length) config.draft.traffic = t
    else delete config.draft.traffic
  },
})

const win = ref('5m')
const live = ref(true)
/** The last read: {devices, counting, since, interval, now}. */
const state = ref(null)

const read = useAsync(async () => {
  const w = win.value
  const r = await api.traffic.devices(w)
  if (w !== win.value) return
  state.value = r
})
onMounted(read.run)
watch(win, () => read.run())
// An apply that switches counting on or off is read at once.
watch(
  () => config.saved?.traffic?.devices,
  () => read.run(),
)

/** The last devices event, which an open device's chart takes its point from. */
const latest = ref(null)
const stream = useTrafficStream({
  // A device's totals move with every read of the connection table, so
  // the list is read again rather than patched.
  devices: (ev) => {
    if (!live.value) return
    latest.value = ev
    read.run()
  },
  reopened: () => read.run(),
})
watch(live, (on) => {
  if (on) read.run()
})

const rows = computed(() => state.value?.devices ?? [])
const { query, shown } = useSearch(rows, (d) => ({
  values: [deviceLabel(d), d.interface, ...(d.addresses ?? [])],
  macs: [d.mac],
}))
const sort = useSort(shown, {
  device: byText((d) => deviceLabel(d)),
  down: byNumber((d) => d.down),
  up: byNumber((d) => d.up),
  seen: byTime((d) => d.lastSeen),
})

const clear = useAsync(async () => {
  await api.traffic.clear()
  await read.run()
})
/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))

const error = computed(
  () => read.error.value || clear.error.value || stream.error.value || state.value?.error,
)
const now = computed(() => (state.value ? Date.parse(state.value.now) / 1000 : 0))
/** The count is read less often than every five seconds on a big table. */
const stretched = computed(() => (state.value?.interval > 5 ? state.value.interval : 0))
const since = computed(() => sinceLine(state.value?.since, win.value, now.value))

const empty = computed(() => {
  if (!state.value) return 'Reading…'
  if (query.value.trim()) return `Nothing matches "${query.value.trim()}".`
  return 'No devices.'
})

const chosen = ref(null)
const open = ref(false)
function show(d) {
  chosen.value = d
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Counting" :locked="auth.readOnly">
      <div class="space-y-3">
        <ToggleRow
          v-model="counting"
          label="Count traffic per device"
          hint="A restart clears it unless System → General writes logs to files."
        />
        <p v-if="counting && state && !state.counting" class="text-ink-muted">
          Apply the draft to start it.
        </p>
      </div>
    </SectionCard>

    <template v-if="state?.counting">
      <div class="flex flex-wrap items-center gap-3">
        <select v-model="win" class="input w-36 max-sm:w-full" aria-label="Window">
          <option v-for="w in WINDOWS" :key="w.value" :value="w.value">{{ w.label }}</option>
        </select>
        <LiveButton v-model="live" :failing="Boolean(error)" />
        <p v-if="stretched" class="text-sm text-ink-muted">Devices every {{ stretched }} s</p>
        <p v-if="since" class="text-sm text-ink-muted">{{ since }}</p>
      </div>

      <SectionCard title="Devices" :count="rows.length" flush>
        <template #actions>
          <SortSelect :sort="sort" :columns="COLUMNS" />
          <ClearLogButton
            name="traffic counts"
            :description="
              inFiles
                ? 'Every device and what it moved is forgotten, and every destination, files included. Counting carries on.'
                : 'Every device and what it moved is forgotten, and every destination. Counting carries on.'
            "
            :busy="clear.busy.value"
            @confirm="clear.run()"
          />
        </template>
        <div class="card-strip flex flex-wrap items-center gap-3">
          <SearchBox
            v-model="query"
            placeholder="name, address, MAC, or interface"
            :shown="shown.length"
            :total="rows.length"
          />
          <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
        </div>
        <!-- On a phone a device is three lines: who, what it moves, and
             where and when. The row's ::before breaks them. -->
        <table class="table table-flow">
          <thead>
            <tr>
              <SortHeader by="device" :sort="sort">Device</SortHeader>
              <SortHeader by="down" :sort="sort" class="text-right">Down</SortHeader>
              <SortHeader by="up" :sort="sort" class="text-right">Up</SortHeader>
              <th>Interface</th>
              <SortHeader by="seen" :sort="sort">Last seen</SortHeader>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!sort.sorted.value.length">
              <td colspan="5" class="text-ink-muted">{{ empty }}</td>
            </tr>
            <tr
              v-for="d in sort.sorted.value"
              :key="d.id"
              class="max-sm:before:order-4 max-sm:before:basis-full max-sm:before:content-['']"
            >
              <td class="max-sm:order-1 max-sm:basis-full">
                <button type="button" class="link font-medium" @click="show(d)">
                  {{ deviceLabel(d) }}
                </button>
                <div v-if="!d.router" class="text-xs text-ink-muted">
                  <template v-if="d.mac">
                    <span class="font-mono">{{ d.mac }}</span>
                    <RandomMacBadge :mac="d.mac" />
                  </template>
                  <span v-else class="font-mono">{{ d.addresses[0] }}</span>
                </div>
              </td>
              <td
                class="text-right tabular-nums max-sm:order-2 max-sm:text-left max-sm:before:mr-1 max-sm:before:content-['↓']"
              >
                {{ formatRate(d.down) }}
                <div class="text-xs text-ink-muted max-sm:inline max-sm:ml-1">
                  {{ formatBytes(d.totals.down) }}
                </div>
              </td>
              <td
                class="text-right tabular-nums max-sm:order-3 max-sm:text-left max-sm:before:mr-1 max-sm:before:content-['↑']"
              >
                {{ formatRate(d.up) }}
                <div class="text-xs text-ink-muted max-sm:inline max-sm:ml-1">
                  {{ formatBytes(d.totals.up) }}
                </div>
              </td>
              <td class="font-mono text-code max-sm:order-5 max-sm:text-ink-muted">
                {{ d.interface }}
              </td>
              <td class="text-xs whitespace-nowrap max-sm:order-6">
                {{ new Date(d.lastSeen).toLocaleString() }}
              </td>
            </tr>
          </tbody>
        </table>
      </SectionCard>
    </template>

    <DeviceDialog
      v-model:open="open"
      :device="chosen"
      :window="win"
      :latest="latest"
      :live="live"
    />
  </div>
</template>
