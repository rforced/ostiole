<script setup>
import { computed } from 'vue'

import RandomMacBadge from '@/components/RandomMacBadge.vue'
import SectionCard from '@/components/SectionCard.vue'
import { formatDuration } from '@/lib/format'

const props = defineProps({
  /** Networks on the air and the clients on them, from GET /overview. */
  wireless: { type: Object, default: () => ({ networks: [], clients: [] }) },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
  /** How many clients to hold room for until then. */
  placeholders: { type: Number, default: 1 },
})

/** Rows past this go behind the link; a dashboard card is not the page. */
const SHOWN = 8

const networks = computed(() => props.wireless?.networks ?? [])
const clients = computed(() => props.wireless?.clients ?? [])
const shown = computed(() => clients.value.slice(0, SHOWN))
const more = computed(() => clients.value.length - shown.value.length)
const reading = computed(() => Math.min(props.placeholders, SHOWN))

function ssidOf(c) {
  return c.ssid || c.interface
}
</script>

<template>
  <SectionCard title="Wireless" flush :aria-busy="loaded ? undefined : 'true'">
    <dl v-if="!loaded" class="card-strip kv" data-reading>
      <dt><span class="skeleton w-20"></span></dt>
      <dd><span class="skeleton w-32"></span></dd>
    </dl>
    <dl v-else class="card-strip kv">
      <template v-for="n in networks" :key="n.interface">
        <dt class="font-mono">{{ n.ssid || n.interface }}</dt>
        <dd>
          {{ n.clients }} client{{ n.clients === 1 ? '' : 's' }}
          <span class="ml-1 font-mono text-code text-ink-muted">{{ n.interface }}</span>
        </dd>
      </template>
    </dl>
    <table class="table">
      <thead>
        <tr>
          <th>Client</th>
          <th>Network</th>
          <th class="text-right">Signal</th>
          <th class="text-right">Connected</th>
        </tr>
      </thead>
      <tbody v-if="!loaded">
        <tr v-if="!reading" data-reading>
          <td colspan="4">
            <span class="sr-only">Reading…</span><span class="skeleton w-20"></span>
          </td>
        </tr>
        <tr v-for="n in reading" :key="n" data-reading>
          <td>
            <span v-if="n === 1" class="sr-only">Reading…</span>
            <div><span class="skeleton w-20"></span></div>
            <div><span class="skeleton w-32"></span></div>
          </td>
          <td><span class="skeleton w-16"></span></td>
          <td class="text-right"><span class="skeleton w-14"></span></td>
          <td class="text-right"><span class="skeleton w-14"></span></td>
        </tr>
      </tbody>
      <tbody v-else>
        <tr v-if="!clients.length">
          <td colspan="4" class="text-ink-muted">No clients.</td>
        </tr>
        <tr v-for="c in shown" :key="c.mac">
          <td>
            <div v-if="c.hostname" class="font-mono">{{ c.hostname }}</div>
            <div class="font-mono text-code" :class="c.hostname ? 'text-ink-muted' : ''">
              {{ c.address || c.mac }}
              <RandomMacBadge v-if="!c.address" :mac="c.mac" />
            </div>
          </td>
          <td class="font-mono text-code">{{ ssidOf(c) }}</td>
          <td class="text-right font-mono text-code">{{ c.signalDbm }} dBm</td>
          <td class="text-right whitespace-nowrap">{{ formatDuration(c.connectedSeconds) }}</td>
        </tr>
      </tbody>
    </table>
    <p class="card-strip border-t border-line">
      <RouterLink to="/wireless#clients" class="link">
        <template v-if="more > 0">And {{ more }} more</template>
        <template v-else>All clients</template>
      </RouterLink>
    </p>
  </SectionCard>
</template>
