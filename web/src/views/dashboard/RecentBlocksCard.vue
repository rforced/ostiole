<script setup>
import SectionCard from '@/components/SectionCard.vue'
import { formatWhen } from '@/lib/format'
import { matchedLabel as by } from '@/lib/fwlog'

defineProps({
  /** The newest refused packets from GET /overview, newest first. */
  blocks: { type: Array, default: () => [] },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
  /** How many rows to hold room for until then. */
  placeholders: { type: Number, default: 1 },
})

function endpoint(addr, port) {
  if (!addr) return '—'
  return port ? `${addr}:${port}` : addr
}

/**
 * An endpoint cut after each colon, where it may break: an IPv4 address
 * only before its port, an IPv6 one at every group. Unbroken, a long IPv6
 * address squeezes By to a word a line.
 */
function pieces(addr, port) {
  return endpoint(addr, port).split(/(?<=:)/)
}
</script>

<template>
  <SectionCard title="Recent blocks" flush :aria-busy="loaded ? undefined : 'true'">
    <table class="table">
      <thead>
        <tr>
          <th>Time</th>
          <th>Source</th>
          <th>Destination</th>
          <th>Rule</th>
        </tr>
      </thead>
      <tbody v-if="!loaded">
        <tr v-if="!placeholders" data-reading>
          <td colspan="4">
            <span class="sr-only">Reading…</span><span class="skeleton w-28"></span>
          </td>
        </tr>
        <tr v-for="n in placeholders" :key="n" data-reading>
          <td>
            <span v-if="n === 1" class="sr-only">Reading…</span>
            <span class="skeleton w-16"></span>
          </td>
          <td><span class="skeleton w-28"></span></td>
          <td><span class="skeleton w-28"></span></td>
          <td>
            <div><span class="skeleton w-24"></span></div>
            <div><span class="skeleton w-20"></span></div>
          </td>
        </tr>
      </tbody>
      <tbody v-else>
        <tr v-if="!blocks.length">
          <td colspan="4" class="text-ink-muted">No blocks.</td>
        </tr>
        <tr v-for="e in blocks" :key="`${e.time}-${e.src}-${e.srcPort}-${e.dst}-${e.dstPort}`">
          <td class="when">
            {{ formatWhen(e.time) }}
          </td>
          <td class="font-mono text-code">
            <template v-for="(p, n) in pieces(e.src, e.srcPort)" :key="n"
              ><wbr v-if="n" />{{ p }}</template
            >
          </td>
          <td class="font-mono text-code">
            <template v-for="(p, n) in pieces(e.dst, e.dstPort)" :key="n"
              ><wbr v-if="n" />{{ p }}</template
            >
          </td>
          <td>
            <div class="font-mono text-code">{{ by(e) }}</div>
            <div class="font-mono text-code whitespace-nowrap text-ink-muted">
              <!-- Whether the sender was told is worth the width: a reject
                   answers back, a drop says nothing. -->
              <span v-if="e.action">{{ e.action }} · </span>{{ e.proto
              }}<span v-if="e.in"> · {{ e.in }}</span>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
    <p class="card-strip border-t border-line">
      <RouterLink to="/firewall/log" class="link">Full log</RouterLink>
    </p>
  </SectionCard>
</template>
