<script setup>
import { computed, onMounted, ref } from 'vue'

import SectionCard from '@/components/SectionCard.vue'
import TrafficChart from '@/components/TrafficChart.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { appendPoint, useTrafficStream } from '@/lib/traffic'

/** Links a dashboard card follows: the external ones, which the overview marks. */
const props = defineProps({
  /** The names of the external links, from GET /overview. */
  links: { type: Array, required: true },
  /** False until every card has been read, this one's charts included. */
  loaded: { type: Boolean, default: true },
  /** How many charts to hold room for until then. */
  placeholders: { type: Number, default: 1 },
})
const emit = defineEmits(['read'])

/**
 * Every link, not only those followed: the card is drawn from the shape
 * the last visit left, before the overview names the links, and a read
 * kept to an empty list stayed empty until the stream reopened.
 */
const charts = ref(null)
const end = ref(0)

const read = useAsync(async () => {
  const ifs = await api.traffic.interfaces('5m')
  charts.value = ifs.links
  end.value = Date.parse(ifs.now) / 1000
})
// The dashboard fills its cards in together, so it waits for the first
// read to answer, with the charts or without.
onMounted(async () => {
  await read.run()
  emit('read')
})

useTrafficStream({
  links: (ev) => {
    const t = Date.parse(ev.time) / 1000
    for (const r of ev.links) {
      const l = charts.value?.find((x) => x.name === r.id)
      if (!l) continue
      l.down = r.down
      l.up = r.up
      l.points = appendPoint(l.points ?? [], [t, r.down, r.up], '5m')
    }
    end.value = t
  },
  reopened: () => read.run(),
})

/** Each followed link and its chart, once read. */
const shown = computed(() =>
  props.links.map((name) => ({ name, chart: charts.value?.find((l) => l.name === name) ?? null })),
)
</script>

<template>
  <SectionCard title="Traffic" flush :aria-busy="loaded ? undefined : 'true'">
    <div class="space-y-4 p-4">
      <p v-if="read.error.value" role="alert" class="text-bad">{{ read.error.value }}</p>
      <template v-if="!loaded">
        <p class="sr-only">Reading…</p>
        <div
          v-for="n in Math.max(placeholders, 1)"
          :key="n"
          class="space-y-1"
          aria-hidden="true"
          data-reading
        >
          <div><span class="skeleton w-14"></span></div>
          <TrafficChart skeleton label="Traffic" />
        </div>
      </template>
      <template v-else>
        <div v-for="l in shown" :key="l.name" class="space-y-1">
          <h3 class="font-mono text-sm font-medium">{{ l.name }}</h3>
          <TrafficChart
            :points="charts ? (l.chart?.points ?? []) : null"
            window="5m"
            :end="end"
            :label="l.name"
            :now="l.chart ? { down: l.chart.down, up: l.chart.up } : null"
          />
        </div>
      </template>
    </div>
    <p class="card-strip border-t border-line">
      <RouterLink to="/traffic" class="link">All traffic</RouterLink>
    </p>
  </SectionCard>
</template>
