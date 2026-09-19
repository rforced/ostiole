<script setup>
import { computed } from 'vue'

import { formatDuration } from '@/lib/format'

const props = defineProps({
  /** Networks on the air and the clients on them, from GET /overview. */
  wireless: { type: Object, default: () => ({ networks: [], clients: [] }) },
})

/** Rows past this go behind the link; a dashboard card is not the page. */
const SHOWN = 8

const networks = computed(() => props.wireless?.networks ?? [])
const clients = computed(() => props.wireless?.clients ?? [])
const shown = computed(() => clients.value.slice(0, SHOWN))
const more = computed(() => clients.value.length - shown.value.length)

function ssidOf(c) {
  return c.ssid || c.interface
}
</script>

<template>
  <section class="card" aria-labelledby="dash-wireless">
    <h2 id="dash-wireless" class="card-title">Wireless</h2>
    <dl class="kv mb-3">
      <template v-for="n in networks" :key="n.interface">
        <dt class="font-mono">{{ n.ssid || n.interface }}</dt>
        <dd>
          {{ n.clients }} client{{ n.clients === 1 ? '' : 's' }}
          <span class="ml-1 font-mono text-code text-neutral-500">{{ n.interface }}</span>
        </dd>
      </template>
    </dl>
    <div class="-mx-4 overflow-x-auto">
      <table class="table">
        <thead>
          <tr>
            <th>Client</th>
            <th>Network</th>
            <th class="text-right">Signal</th>
            <th class="text-right">Connected</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!clients.length" key="empty" class="row-static">
            <td colspan="4" class="text-neutral-500">No clients.</td>
          </tr>
          <tr v-for="c in shown" :key="c.mac">
            <td>
              <div v-if="c.hostname" class="font-mono">{{ c.hostname }}</div>
              <div class="font-mono text-code" :class="c.hostname ? 'text-neutral-500' : ''">
                {{ c.address || c.mac }}
              </div>
            </td>
            <td class="font-mono text-code">{{ ssidOf(c) }}</td>
            <td class="text-right font-mono text-code">{{ c.signalDbm }} dBm</td>
            <td class="text-right whitespace-nowrap">{{ formatDuration(c.connectedSeconds) }}</td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
    <p class="mt-3">
      <RouterLink to="/wireless#clients" class="link">
        <template v-if="more > 0">And {{ more }} more</template>
        <template v-else>All clients</template>
      </RouterLink>
    </p>
  </section>
</template>
