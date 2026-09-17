<script setup>
import { RefreshCw } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const rows = ref([])
const error = ref('')
const busy = ref(false)
const search = ref('')

onMounted(load)

async function load() {
  busy.value = true
  error.value = ''
  try {
    rows.value = await api.diagnostics.neighbours()
  } catch (e) {
    rows.value = []
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

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
  <div class="space-y-3">
    <p class="max-w-3xl text-sm text-neutral-500">
      Which address is at which hardware address, on which interface: ARP for IPv4 and neighbour
      discovery for IPv6. It is the quickest way to tell whether a host is actually on the segment
      you think it is.
    </p>

    <div class="flex flex-wrap items-center gap-3">
      <label class="sr-only" for="nb-search">Search</label>
      <input
        id="nb-search"
        v-model="search"
        class="input w-64 font-mono"
        placeholder="address, MAC, or interface"
        spellcheck="false"
      />
      <button type="button" class="btn-secondary" :disabled="busy" @click="load">
        <RefreshCw class="mr-1 size-4" aria-hidden="true" /> Refresh
      </button>
      <span class="text-sm text-neutral-500">{{ shown.length }} of {{ rows.length }}</span>
    </div>

    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>Address</th>
            <th>Hardware address</th>
            <th>Interface</th>
            <th>Family</th>
            <th>State</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!shown.length">
            <td colspan="5" class="text-neutral-500">Nothing to show.</td>
          </tr>
          <tr v-for="(n, i) in shown" :key="`${n.interface}-${n.address}-${i}`">
            <td class="font-mono text-xs">
              {{ n.address }}
              <span v-if="n.router" class="badge ml-1">router</span>
            </td>
            <td class="font-mono text-xs">
              {{ n.mac || '—' }}
              <span v-if="named[(n.mac ?? '').toLowerCase()]" class="ml-1 text-neutral-500">
                {{ named[n.mac.toLowerCase()] }}
              </span>
            </td>
            <td class="font-mono text-xs">{{ n.interface }}</td>
            <td class="text-xs">{{ n.family }}</td>
            <td class="text-xs">
              <span class="badge" :class="tone(n.state)">{{ n.state }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
