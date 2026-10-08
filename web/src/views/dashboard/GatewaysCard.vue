<script setup>
import SectionCard from '@/components/SectionCard.vue'
import { neverHint, stateOf } from '@/lib/gateways'
import GatewayStrip from '@/views/dashboard/GatewayStrip.vue'

const props = defineProps({
  /** Gateway health from GET /overview. */
  gateways: { type: Array, default: () => [] },
  /**
   * Default routes the kernel has that no gateway covers. They carry
   * traffic without anyone checking they still answer, which is worth
   * seeing next to the ones that are watched.
   */
  unwatched: { type: Array, default: () => [] },
  /** Each gateway's last day, from GET /gateways/strips; null until read, empty where none is kept. */
  strips: { type: Array, default: null },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
  /** How many rows to hold room for until then. */
  placeholders: { type: Number, default: 1 },
})

const stripOf = (g) => props.strips?.find((s) => s.name === g.name) ?? null
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
        <template v-for="n in Math.max(placeholders, 1)" :key="n">
          <tr data-reading>
            <td>
              <span v-if="n === 1" class="sr-only">Reading…</span>
              <div><span class="skeleton w-16"></span></div>
              <div><span class="skeleton w-32"></span></div>
            </td>
            <td><span class="skeleton h-5 w-10 rounded-full"></span></td>
            <td class="text-right"><span class="skeleton w-14"></span></td>
            <td class="text-right"><span class="skeleton w-8"></span></td>
          </tr>
          <tr aria-hidden="true">
            <td colspan="4" class="pt-0"><GatewayStrip label="" skeleton /></td>
          </tr>
        </template>
      </tbody>
      <tbody v-else>
        <template v-for="g in gateways" :key="g.name">
          <tr>
            <td>
              <div>
                <span class="font-mono font-medium">{{ g.name }}</span>
                <span v-if="g.active" class="badge badge-ok ml-1">active</span>
              </div>
              <div class="font-mono text-code text-ink-muted">
                {{ g.interface }}<span v-if="g.tunnel" class="font-sans"> · through the tunnel</span
                ><span v-else-if="g.address"> · {{ g.address }}</span>
              </div>
            </td>
            <td>
              <span
                class="badge"
                :class="{ 'badge-ok': g.online, 'badge-warn': stateOf(g) === 'down' }"
                >{{ stateOf(g) }}</span
              >
              <span v-if="g.slow?.length" class="badge badge-warn ml-1">slow</span>
              <span v-if="g.lossy?.length" class="badge badge-warn ml-1">losing packets</span>
              <div v-if="g.neverAnswered" class="mt-1 text-xs text-ink-muted">
                {{ neverHint({ monitor: stripOf(g)?.monitor }) }}
              </div>
            </td>
            <td class="text-right font-mono text-code">
              <template v-if="g.families?.length > 1">
                <div v-for="f in g.families" :key="f.family">
                  {{ f.family }} {{ f.latencyMs.toFixed(1) }} ms
                </div>
              </template>
              <template v-else>{{ g.latencyMs.toFixed(1) }} ms</template>
            </td>
            <td class="text-right font-mono text-code">
              <template v-if="g.families?.length > 1">
                <div v-for="f in g.families" :key="f.family">
                  {{ f.family }} {{ f.lossPercent.toFixed(0) }}%
                </div>
              </template>
              <template v-else>{{ g.lossPercent.toFixed(0) }}%</template>
            </td>
          </tr>
          <tr v-if="strips === null || stripOf(g)" data-strip>
            <td colspan="4" class="pt-0">
              <GatewayStrip :strip="stripOf(g)" :label="g.name" :skeleton="strips === null" />
            </td>
          </tr>
        </template>
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
        A route nobody watches keeps no history and cannot fail over.
        <RouterLink to="/routing" class="link">Add it under Routing</RouterLink>
      </template>
      <RouterLink v-else to="/routing" class="link">Manage gateways</RouterLink>
    </p>
  </SectionCard>
</template>
