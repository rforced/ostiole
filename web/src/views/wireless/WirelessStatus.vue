<script setup>
import { computed, ref } from 'vue'

import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

/** How often the radios are asked what they are doing while the page is shown. */
const POLL_MS = 5000

const config = useConfigStore()
const status = ref(null)

useAsync(
  async () => {
    status.value = await api.wireless.radios()
  },
  { interval: POLL_MS, immediate: true },
)

/** What the card says, in the order a router goes through. */
const stage = computed(() => {
  if (!status.value) return 'loading'
  if (!status.value.setUp) return 'missing'
  if (!status.value.radios.length) return 'no-radios'
  if (!config.wirelessCountry) return 'no-country'
  return 'ready'
})

/** The badge beside each radio, per what it is doing. */
const RUNNING = { text: 'on air', tone: 'badge-ok' }
const STOPPED = { text: 'stopped', tone: 'badge-warn' }
const OFF = { text: 'off', tone: '' }

/** One line per configured radio, merged with what the card reports. */
const rows = computed(() =>
  config.radios.map((r) => {
    const live = (status.value?.radios ?? []).find((c) => c.name === r.name)
    const networks = config.wirelessNetworks.filter((i) => i.wireless.radio === r.name && i.enabled)
    return {
      name: r.name,
      band: r.band,
      power: r.power,
      channel: r.channel,
      width: r.width,
      running: live?.running ?? null,
      networks,
      badge: !r.enabled || !networks.length ? OFF : live?.running ? RUNNING : STOPPED,
    }
  }),
)

const BANDS = { '2g': '2.4 GHz', '5g': '5 GHz', '6g': '6 GHz' }

/** The channel a radio settled on, which is not always the one it was given. */
function channelOf(row) {
  if (row.running) return `${row.running.channel} · ${row.running.width} MHz`
  return row.channel ? `${row.channel} · ${row.width} MHz` : `automatic · ${row.width} MHz`
}

function clientsOf(row, name) {
  const live = row.running?.networks?.find((n) => n.interface === name)
  return live ? `${live.clients} client${live.clients === 1 ? '' : 's'}` : ''
}
</script>

<template>
  <div v-if="status">
    <p
      v-if="stage === 'missing'"
      class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
      role="note"
    >
      Not on this router. Run <code class="font-mono">ostiole repair --wireless</code> as root once.
    </p>
    <p v-else-if="stage === 'no-radios'" class="text-sm text-neutral-500">
      No radios on this router.
    </p>
    <p v-else-if="stage === 'no-country'" class="text-sm text-neutral-500">
      Set the country on the Radios tab.
    </p>
    <p v-else-if="!rows.length" class="text-sm text-neutral-500">No radio is configured.</p>

    <section v-else class="card space-y-3" aria-label="Radios">
      <h2 class="section-title">Radios</h2>
      <div v-for="row in rows" :key="row.name" class="space-y-1">
        <h3 class="font-mono font-medium">
          {{ row.name }}
          <span class="badge ml-2" :class="row.badge.tone">{{ row.badge.text }}</span>
        </h3>
        <dl class="kv">
          <dt>Band</dt>
          <dd>{{ BANDS[row.band] ?? row.band }}</dd>
          <dt>Channel</dt>
          <dd>{{ channelOf(row) }}</dd>
          <template v-if="row.running">
            <dt>Power</dt>
            <dd>{{ row.running.txPower }} dBm</dd>
          </template>
          <template v-if="row.networks.length">
            <dt>Networks</dt>
            <dd>
              <span v-for="n in row.networks" :key="n.name" class="mr-3">
                {{ n.wireless.ssid }}
                <span class="text-neutral-500">{{ clientsOf(row, n.name) }}</span>
              </span>
            </dd>
          </template>
        </dl>
      </div>
    </section>
  </div>
</template>
