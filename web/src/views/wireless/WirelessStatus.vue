<script setup>
import { computed, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import SectionCard from '@/components/SectionCard.vue'
import StatusBadge from '@/components/StatusBadge.vue'

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
      stopped: live?.stopped ?? null,
      networks,
      state: !r.enabled || !networks.length ? 'off' : live?.running ? 'running' : 'stopped',
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
    <AppNotice v-if="stage === 'missing'">
      Not on this router. Run <code class="font-mono">ostiole repair --wireless</code> as root once.
    </AppNotice>
    <p v-else-if="stage === 'no-radios'" class="text-sm text-ink-muted">
      No radios on this router.
    </p>
    <p v-else-if="stage === 'no-country'" class="text-sm text-ink-muted">
      Set the country on the Radios tab.
    </p>
    <p v-else-if="!rows.length" class="text-sm text-ink-muted">No radio is configured.</p>

    <SectionCard v-else title="Radios" :count="rows.length">
      <div class="space-y-4">
        <fieldset v-for="row in rows" :key="row.name" class="space-y-1">
          <legend class="mb-1 flex items-center gap-2 font-mono font-medium">
            {{ row.name }}
            <StatusBadge :state="row.state" />
          </legend>
          <dl class="kv">
            <dt>Band</dt>
            <dd>{{ BANDS[row.band] ?? row.band }}</dd>
            <dt>Channel</dt>
            <dd>{{ channelOf(row) }}</dd>
            <template v-if="row.running">
              <dt>Power</dt>
              <dd>{{ row.running.txPower }} dBm</dd>
            </template>
            <template v-if="!row.running && row.stopped">
              <dt>Last log line</dt>
              <dd class="font-mono text-code">{{ row.stopped.reason }}</dd>
            </template>
            <template v-if="row.networks.length">
              <dt>Networks</dt>
              <dd>
                <span v-for="n in row.networks" :key="n.name" class="mr-3">
                  {{ n.wireless.ssid }}
                  <span class="text-ink-muted">{{ clientsOf(row, n.name) }}</span>
                </span>
              </dd>
            </template>
          </dl>
        </fieldset>
      </div>
    </SectionCard>
  </div>
</template>
