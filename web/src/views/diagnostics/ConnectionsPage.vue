<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import LiveButton from '@/components/LiveButton.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes, formatCount } from '@/lib/format'
import { LEVELS, levelOf } from '@/lib/meter'
import { byAddress, byNumber, byText, useSort } from '@/lib/sort'

const POLL_MS = 5000
/** How long typing rests before the router is asked. */
const SETTLE_MS = 250

/**
 * A full connection table drops packets and says so once in dmesg, which
 * is nowhere anybody looks, so the count is shown against the ceiling the
 * kernel is enforcing, on the same meter as the dashboard uses.
 */

const COLUMNS = [
  ['protocol', 'Protocol'],
  ['from', 'From'],
  ['to', 'To'],
  ['leaves', 'Leaves as'],
  ['state', 'State'],
  ['traffic', 'Traffic'],
  ['expires', 'Expires in'],
]

const result = ref(null)
const live = ref(true)
/**
 * The router filters, since the table runs to hundreds of thousands and
 * only the busiest come back: each word in what a row shows, or a network
 * like 10.0.0.0/8 holding either end.
 */
const query = ref('')

const load = useAsync(
  async () => {
    result.value = await api.diagnostics.states({ q: query.value.trim() })
  },
  // Live owns the poll, and starts on.
  { interval: POLL_MS, immediate: true },
)

let settle = 0
watch(query, () => {
  window.clearTimeout(settle)
  settle = window.setTimeout(load.run, SETTLE_MS)
})
onBeforeUnmount(() => window.clearTimeout(settle))
watch(live, (on) => {
  if (on) {
    load.run()
    load.start()
  } else {
    load.stop()
  }
})

/** Seconds in a Go duration as the server writes it: 1h2m3s. */
function seconds(ttl) {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/.exec(ttl ?? '')
  if (!m || !ttl) return null
  return Number(m[1] ?? 0) * 3600 + Number(m[2] ?? 0) * 60 + Number(m[3] ?? 0)
}

/** The server sends the busiest first, and the table opens that way. */
const sort = useSort(
  () => result.value?.states ?? [],
  {
    protocol: byText((s) => s.protocol),
    from: byAddress((s) => s.source),
    to: byAddress((s) => s.destination),
    leaves: byAddress((s) => (s.nat ? s.replyDest : null)),
    state: byText((s) => s.state),
    traffic: byNumber((s) => s.bytes),
    expires: byNumber((s) => seconds(s.ttl), 'asc'),
  },
  { by: 'traffic' },
)
const states = sort.sorted

/** Protocol counts across the whole table, not just the rows shown. */
const protocols = computed(() =>
  Object.entries(result.value?.byProtocol ?? {}).sort((a, b) => b[1] - a[1]),
)

const trimmed = computed(() => result.value && result.value.matched > result.value.states.length)

/**
 * How full the table is, or null on a kernel that will not say what its
 * ceiling is — an older one, or the module unloaded.
 */
const used = computed(() => {
  const r = result.value
  if (!r?.max) return null
  return Math.min(100, (r.total / r.max) * 100)
})

const level = computed(() => levelOf(used.value))

function endpoint(address, port) {
  if (!address) return '—'
  return port ? `${address}:${port}` : address
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Connections"
      :count="result?.matched ?? 0"
      intro="Every connection the kernel is tracking, which the established rules match against."
      flush
    >
      <template #actions>
        <SortSelect :sort="sort" :columns="COLUMNS" />
        <LiveButton v-model="live" :failing="Boolean(load.error.value)" />
      </template>
      <div class="card-strip space-y-3">
        <SearchBox
          v-model="query"
          placeholder="address, network, port, or state"
          :shown="result?.matched ?? 0"
          :total="result ? result.total : null"
        />

        <p v-if="load.error.value" role="alert" class="text-bad">{{ load.error.value }}</p>

        <div v-if="result" class="max-w-md space-y-1">
          <div v-if="used !== null" class="flex items-baseline justify-between">
            <span class="text-ink-muted">Connection table</span>
            <span>
              <span class="font-medium tabular-nums">{{ used.toFixed(0) }}%</span>
              <span class="ml-2 text-ink-muted tabular-nums">
                {{ formatCount(result.total) }} of {{ formatCount(result.max) }}
              </span>
            </span>
          </div>
          <div
            v-if="used !== null"
            class="meter"
            :class="LEVELS[level].track"
            role="meter"
            aria-label="Connection table usage"
            :aria-valuenow="Math.round(used)"
            aria-valuemin="0"
            aria-valuemax="100"
          >
            <div
              class="meter-fill"
              :class="LEVELS[level].fill"
              :style="{ width: `${used}%` }"
            ></div>
          </div>
          <p v-if="level !== 'ok'" :class="level === 'critical' ? 'text-bad' : 'text-warn'">
            The kernel drops packets once the table is full. The ceiling is set under Firewall,
            Protection.
          </p>
        </div>

        <p v-if="result" class="text-ink-muted">
          {{ result.total }} connection(s) tracked<span v-if="trimmed">
            · showing the {{ result.states.length }} busiest</span
          >
          <template v-if="protocols.length">
            ·
            <span v-for="([name, count], i) in protocols" :key="name">
              <span v-if="i">, </span>{{ count }} {{ name }}
            </span>
          </template>
        </p>
      </div>

      <!-- On a phone a connection is two lines: who talks to whom; what
           became of it. -->
      <table class="table table-flow">
        <thead>
          <tr>
            <SortHeader by="protocol" :sort="sort">Protocol</SortHeader>
            <SortHeader by="from" :sort="sort">From</SortHeader>
            <SortHeader by="to" :sort="sort">To</SortHeader>
            <SortHeader by="leaves" :sort="sort">Leaves as</SortHeader>
            <SortHeader by="state" :sort="sort">State</SortHeader>
            <SortHeader by="traffic" :sort="sort">Traffic</SortHeader>
            <SortHeader by="expires" :sort="sort">Expires in</SortHeader>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!states.length">
            <td colspan="7" class="text-ink-muted">
              {{
                load.busy.value && !result
                  ? 'Reading…'
                  : query.trim()
                    ? `Nothing matches "${query.trim()}".`
                    : 'No connections.'
              }}
            </td>
          </tr>
          <tr
            v-for="(s, i) in states"
            :key="i"
            class="max-sm:after:order-4 max-sm:after:basis-full max-sm:after:content-['']"
          >
            <td class="font-mono text-code max-sm:order-1">{{ s.protocol }}</td>
            <td class="font-mono text-code max-sm:order-2">
              {{ endpoint(s.source, s.sourcePort) }}
            </td>
            <td
              class="font-mono text-code max-sm:order-3 max-sm:before:mr-2 max-sm:before:content-['→']"
            >
              {{ endpoint(s.destination, s.destPort) }}
            </td>
            <td class="font-mono text-code max-sm:order-5">
              <template v-if="s.nat">
                {{ endpoint(s.replyDest, s.replyDestPort) }}
                <span class="badge">NAT</span>
              </template>
              <span v-else class="text-ink-muted">not translated</span>
            </td>
            <td class="font-mono text-code max-sm:order-6">
              {{ s.state || '—'
              }}<span v-if="s.mark" class="ml-1 badge" :title="`Packet mark ${s.mark}`">
                routed
              </span>
            </td>
            <td class="font-mono text-code whitespace-nowrap max-sm:order-7">
              {{ formatBytes(s.bytes) }} · {{ s.packets }}p
            </td>
            <td
              class="font-mono text-code max-sm:order-8 max-sm:text-ink-muted max-sm:before:content-['expires_in_']"
            >
              {{ s.ttl }}
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
