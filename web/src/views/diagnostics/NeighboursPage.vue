<script setup>
import { computed, onMounted, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const rows = ref([])
const search = ref('')

const load = useAsync(async () => {
  rows.value = await api.diagnostics.neighbours()
})
onMounted(load.run)

/** A static lease turns a hardware address into a name we already know. */
const named = computed(() => {
  const out = {}
  for (const l of config.draft?.services?.dhcp?.staticLeases ?? []) {
    if (l.mac && l.hostname) out[l.mac.toLowerCase()] = l.hostname
  }
  return out
})

const shown = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return rows.value
  return rows.value.filter((n) =>
    [n.address, n.mac, n.interface, named.value[(n.mac ?? '').toLowerCase()]]
      .filter(Boolean)
      .some((v) => v.toLowerCase().includes(q)),
  )
})

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
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run"
        />
      </template>
      <div class="card-strip flex flex-wrap items-center gap-3">
        <label class="sr-only" for="nb-search">Search</label>
        <input
          id="nb-search"
          v-model="search"
          class="input w-64 font-mono max-sm:w-full"
          placeholder="address, MAC, or interface"
          spellcheck="false"
        />
        <span class="text-ink-muted">{{ shown.length }} of {{ rows.length }}</span>
        <p v-if="load.error.value" role="alert" class="text-bad">{{ load.error.value }}</p>
      </div>

      <!-- On a phone a neighbour is two lines: the address and its state; the
           link layer. -->
      <table class="table table-flow">
        <thead>
          <tr>
            <th>Address</th>
            <th>MAC</th>
            <th>Interface</th>
            <th>Family</th>
            <th>State</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!shown.length">
            <td colspan="5" class="text-ink-muted">
              {{ load.busy.value && !rows.length ? 'Reading…' : 'No neighbours.' }}
            </td>
          </tr>
          <tr
            v-for="(n, i) in shown"
            :key="`${n.interface}-${n.address}-${i}`"
            class="max-sm:after:order-3 max-sm:after:basis-full max-sm:after:content-['']"
          >
            <td class="font-mono text-code max-sm:order-1">
              {{ n.address }}
              <span v-if="n.router" class="badge ml-1">router</span>
            </td>
            <td class="font-mono text-code max-sm:order-4">
              {{ n.mac || '—' }}
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
