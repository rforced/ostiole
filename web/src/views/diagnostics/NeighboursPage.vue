<script setup>
import { computed, ref, watch } from 'vue'

import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import RandomMacBadge from '@/components/RandomMacBadge.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { useSearch } from '@/lib/search'
import { byAddress, byText, useSort } from '@/lib/sort'
import { useConfigStore } from '@/stores/config'

const COLUMNS = [
  ['address', 'Address'],
  ['mac', 'MAC'],
  ['interface', 'Interface'],
  ['family', 'Family'],
  ['state', 'State'],
]

/** How often Live reads the tables again. */
const LIVE_MS = 2000

const config = useConfigStore()
const rows = ref([])
const live = ref(true)

const load = useAsync(
  async () => {
    rows.value = await api.diagnostics.neighbours()
  },
  // Live owns the poll, and starts on.
  { interval: LIVE_MS, immediate: true },
)
watch(live, (on) => {
  if (on) {
    load.run()
    load.start()
  } else {
    load.stop()
  }
})

/** A static lease turns a hardware address into a name we already know. */
const named = computed(() => {
  const out = {}
  for (const l of config.draft?.services?.dhcp?.staticLeases ?? []) {
    if (l.mac && l.hostname) out[l.mac.toLowerCase()] = l.hostname
  }
  return out
})

const { query, shown } = useSearch(rows, (n) => ({
  values: [n.address, n.interface, named.value[(n.mac ?? '').toLowerCase()]],
  macs: [n.mac],
}))

const sort = useSort(
  shown,
  {
    address: byAddress((n) => n.address),
    mac: byText((n) => n.mac),
    interface: byText((n) => n.interface),
    family: byText((n) => n.family),
    state: byText((n) => n.state),
  },
  { by: 'interface', tie: 'address' },
)
const sorted = sort.sorted

const empty = computed(() =>
  emptyText(
    load,
    rows.value.length ? `Nothing matches "${query.value.trim()}".` : 'No neighbours.',
  ),
)

/** REACHABLE is current; FAILED means the address answered nothing. */
function tone(state) {
  if (state.includes('REACHABLE') || state.includes('PERMANENT')) return 'badge-ok'
  if (state.includes('FAILED') || state.includes('INCOMPLETE')) return 'badge-warn'
  return ''
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Neighbours"
      :count="rows.length"
      intro="The ARP and NDP tables: what this router has seen answer on each link."
      flush
    >
      <template #actions>
        <SortSelect :sort="sort" :columns="COLUMNS" />
        <LiveButton v-model="live" :failing="Boolean(load.error.value)" />
      </template>
      <div class="card-strip-row">
        <SearchBox
          v-model="query"
          placeholder="address, MAC, or interface"
          :shown="shown.length"
          :total="rows.length"
        />
        <ErrorLine v-if="load.error.value">{{ load.error.value }}</ErrorLine>
      </div>

      <!-- On a phone a neighbour is two lines: the address and its state; the
           link layer. -->
      <table class="table table-flow">
        <thead>
          <tr>
            <SortHeader by="address" :sort="sort">Address</SortHeader>
            <SortHeader by="mac" :sort="sort">MAC</SortHeader>
            <SortHeader by="interface" :sort="sort">Interface</SortHeader>
            <SortHeader by="family" :sort="sort">Family</SortHeader>
            <SortHeader by="state" :sort="sort">State</SortHeader>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!sorted.length">
            <td colspan="5" class="text-ink-muted">{{ empty }}</td>
          </tr>
          <tr
            v-for="(n, i) in sorted"
            :key="`${n.interface}-${n.address}-${i}`"
            class="max-sm:after:order-3 max-sm:after:basis-full max-sm:after:content-['']"
          >
            <td class="font-mono text-code max-sm:order-1">
              {{ n.address }}
              <span v-if="n.router" class="badge ml-1">router</span>
            </td>
            <td class="font-mono text-code max-sm:order-4">
              {{ n.mac || '—' }}
              <RandomMacBadge :mac="n.mac" />
              <span v-if="named[(n.mac ?? '').toLowerCase()]" class="ml-1 text-ink-muted">
                {{ named[n.mac.toLowerCase()] }}
              </span>
            </td>
            <td class="font-mono text-code max-sm:order-5">{{ n.interface }}</td>
            <td class="max-sm:order-6 max-sm:text-ink-muted">{{ n.family }}</td>
            <td class="max-sm:order-2">
              <span class="badge" :class="tone(n.state)">{{ n.state.toLowerCase() }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
