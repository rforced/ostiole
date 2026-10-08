<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import RandomMacBadge from '@/components/RandomMacBadge.vue'
import TrafficChart from '@/components/TrafficChart.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes } from '@/lib/format'
import { DESTINATIONS_FOR, WINDOWS, appendPoint, deviceLabel, serviceLabel } from '@/lib/traffic'

/**
 * One device over the tab's window: its chart, how it is known, and while
 * destinations are recorded the five it moved the most with. Over five
 * minutes its chart takes each read of the connection table from the
 * tab's stream.
 */
const props = defineProps({
  /** A row of the Devices tab. */
  device: { type: Object, default: null },
  window: { type: String, default: '5m' },
  /** The tab's last devices event. */
  latest: { type: Object, default: null },
  live: { type: Boolean, default: true },
})
const open = defineModel('open', { type: Boolean, default: false })

const data = ref(null)
const end = ref(0)
const read = useAsync(async () => {
  const id = props.device.id
  const r = await api.traffic.device(id, props.window)
  if (id !== props.device?.id) return
  data.value = r
  end.value = r.points?.at(-1)?.[0] ?? 0
})
/** Its top destinations, null while they are not recorded. */
const top = ref(null)
const readTop = useAsync(async () => {
  const id = props.device.id
  const r = await api.traffic.destinations({
    device: id,
    window: DESTINATIONS_FOR[props.window],
    limit: 5,
  })
  if (id !== props.device?.id) return
  top.value = r.enabled ? (r.entries ?? []) : null
})
watch(
  () => [open.value, props.device?.id, props.window],
  () => {
    if (!open.value || !props.device) return
    if (data.value?.id !== props.device.id) {
      data.value = null
      top.value = null
    }
    read.run()
    readTop.run()
  },
  { immediate: true },
)

watch(
  () => props.latest,
  (ev) => {
    if (!open.value || !ev || !data.value || props.window !== '5m' || !props.live) return
    const r = ev.devices?.find((x) => x.id === data.value.id)
    if (!r) return
    const t = Date.parse(ev.time) / 1000
    data.value = {
      ...data.value,
      down: r.down,
      up: r.up,
      points: appendPoint(data.value.points ?? [], [t, r.down, r.up], '5m'),
    }
    end.value = t
  },
)

const title = computed(() => (props.device ? deviceLabel(props.device) : ''))
const windowLabel = computed(() => WINDOWS.find((w) => w.value === props.window)?.label ?? '')
</script>

<template>
  <AppDialog v-model:open="open" :title="title" :read-only="false">
    <div v-if="device" class="space-y-4">
      <TrafficChart
        :points="data ? (data.points ?? []) : null"
        :window="window"
        :end="end"
        :label="title"
        :now="data ? { down: data.down, up: data.up } : null"
        :stale="read.busy.value && data !== null"
      />
      <p v-if="read.error.value" role="alert" class="text-sm text-bad">{{ read.error.value }}</p>
      <dl class="kv text-sm">
        <template v-if="device.mac">
          <dt>MAC</dt>
          <dd>
            <span class="font-mono">{{ device.mac }}</span>
            <RandomMacBadge :mac="device.mac" />
          </dd>
        </template>
        <template v-if="!device.router">
          <dt>Addresses</dt>
          <dd class="font-mono">
            <div v-for="a in device.addresses" :key="a">{{ a }}</div>
          </dd>
        </template>
        <template v-if="device.interface">
          <dt>Interface</dt>
          <dd class="font-mono">{{ device.interface }}</dd>
        </template>
        <dt>In {{ windowLabel }}</dt>
        <dd class="tabular-nums">
          {{ formatBytes(data?.totals?.down ?? device.totals.down) }} down,
          {{ formatBytes(data?.totals?.up ?? device.totals.up) }} up
        </dd>
      </dl>
      <div v-if="top?.length" class="space-y-2">
        <h3 class="group-title">Top destinations</h3>
        <table class="table">
          <thead>
            <tr>
              <th>Destination</th>
              <th>Service</th>
              <th class="num">Down</th>
              <th class="num">Up</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in top" :key="`${r.destination} ${r.protocol} ${r.port}`">
              <td class="max-w-56 truncate">{{ r.destination }}</td>
              <td>{{ serviceLabel(r) }}</td>
              <td class="num">{{ formatBytes(r.down) }}</td>
              <td class="num">{{ formatBytes(r.up) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </AppDialog>
</template>
