<script setup>
import SectionCard from '@/components/SectionCard.vue'
defineProps({
  /** Gateway health from GET /overview. */
  gateways: { type: Array, default: () => [] },
  /**
   * Default routes the kernel has that no gateway covers. They carry
   * traffic without anyone checking they still answer, which is worth
   * seeing next to the ones that are watched.
   */
  unwatched: { type: Array, default: () => [] },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
  /** How many rows to hold room for until then. */
  placeholders: { type: Number, default: 1 },
})
</script>

<template>
  <SectionCard title="Gateways" flush :aria-busy="loaded ? undefined : 'true'">
    <table class="table">
      <thead>
        <tr>
          <th>Gateway</th>
          <th>State</th>
          <th class="text-right">Latency</th>
          <th class="text-right">Loss</th>
        </tr>
      </thead>
      <tbody v-if="!loaded">
        <tr v-for="n in Math.max(placeholders, 1)" :key="n" data-reading>
          <td>
            <span v-if="n === 1" class="sr-only">Reading…</span>
            <div><span class="skeleton w-16"></span></div>
            <div><span class="skeleton w-32"></span></div>
          </td>
          <td><span class="skeleton h-5 w-10 rounded-full"></span></td>
          <td class="text-right"><span class="skeleton w-14"></span></td>
          <td class="text-right"><span class="skeleton w-8"></span></td>
        </tr>
      </tbody>
      <tbody v-else>
        <tr v-for="g in gateways" :key="g.name">
          <td>
            <div>
              <span class="font-mono font-medium">{{ g.name }}</span>
              <span v-if="g.active" class="badge badge-ok ml-1">active</span>
            </div>
            <div class="font-mono text-code text-ink-muted">
              {{ g.interface }}<span v-if="g.address"> · {{ g.address }}</span>
            </div>
          </td>
          <td>
            <span v-if="g.unknown" class="badge">probing</span>
            <span v-else class="badge" :class="g.online ? 'badge-ok' : 'badge-warn'">
              {{ g.online ? 'up' : 'down' }}
            </span>
          </td>
          <td class="text-right font-mono text-code">{{ g.latencyMs.toFixed(1) }} ms</td>
          <td class="text-right font-mono text-code">{{ g.lossPercent.toFixed(0) }}%</td>
        </tr>
        <tr
          v-for="d in unwatched"
          :key="`${d.interface}-${d.address}-${d.family}`"
          data-unwatched
          class="text-ink-muted"
        >
          <td>
            <div class="font-mono font-medium">{{ d.interface }}</div>
            <div class="font-mono text-code">{{ d.address }} · {{ d.protocol }}</div>
          </td>
          <td><span class="badge">not watched</span></td>
          <td class="text-right">—</td>
          <td class="text-right">—</td>
        </tr>
      </tbody>
    </table>
    <p v-if="!loaded" class="card-strip border-t border-line">
      <span class="skeleton w-32"></span>
    </p>
    <p v-else class="card-strip border-t border-line">
      <template v-if="unwatched.length">
        A route nobody watches cannot fail over.
        <RouterLink to="/routing" class="link">Watch it under Routing</RouterLink>
      </template>
      <RouterLink v-else to="/routing" class="link">Manage gateways</RouterLink>
    </p>
  </SectionCard>
</template>
