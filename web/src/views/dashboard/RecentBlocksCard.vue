<script setup>
import SectionCard from '@/components/SectionCard.vue'
defineProps({
  /** The newest refused packets from GET /overview, newest first. */
  blocks: { type: Array, default: () => [] },
})

function endpoint(addr, port) {
  if (!addr) return '—'
  return port ? `${addr}:${port}` : addr
}

/** What refused the packet: a rule by id, the zone or default policy, or
 *  one of the blocked-source guards. */
function by(e) {
  if (e.kind === 'rule') return e.ruleId
  if (e.kind === 'zone-drop') return `${e.zone} default`
  if (e.kind === 'block-private') return 'private source'
  if (e.kind === 'block-bogons') return 'bogon source'
  return 'default drop'
}
</script>

<template>
  <SectionCard title="Recent blocks" flush>
    <table class="table">
      <thead>
        <tr>
          <th>Time</th>
          <th>Source</th>
          <th>Destination</th>
          <th>By</th>
        </tr>
      </thead>
      <TransitionGroup name="row" tag="tbody">
        <tr v-if="!blocks.length" key="empty" class="row-static">
          <td colspan="4" class="text-ink-muted">Nothing refused lately.</td>
        </tr>
        <tr v-for="e in blocks" :key="`${e.time}-${e.src}-${e.srcPort}-${e.dst}-${e.dstPort}`">
          <td class="text-xs whitespace-nowrap text-ink-muted">
            {{ new Date(e.time).toLocaleTimeString() }}
          </td>
          <td class="font-mono text-code">{{ endpoint(e.src, e.srcPort) }}</td>
          <td class="font-mono text-code">{{ endpoint(e.dst, e.dstPort) }}</td>
          <td>
            <div class="font-mono text-code">{{ by(e) }}</div>
            <div class="font-mono text-code text-ink-muted">
              <!-- Whether the sender was told is worth the width: a reject
                   answers back, a drop says nothing. -->
              <span v-if="e.action">{{ e.action }} · </span>{{ e.proto
              }}<span v-if="e.in"> · {{ e.in }}</span>
            </div>
          </td>
        </tr>
      </TransitionGroup>
    </table>
    <p class="border-t border-line px-4 py-3">
      <RouterLink to="/firewall/log" class="link">Full log</RouterLink>
    </p>
  </SectionCard>
</template>
