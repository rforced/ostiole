<script setup>
import { Pause, Play, Trash2 } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import { ApiError, api } from '@/lib/api'

const MAX_ROWS = 500

const entries = ref([])
const paused = ref(false)
const filter = ref('')
const error = ref('')
const connected = ref(false)
let source = null
let pending = []

function push(list) {
  entries.value = [...list, ...entries.value].slice(0, MAX_ROWS)
}

function connect() {
  // EventSource sends the session cookie on same-origin requests.
  source = new EventSource('/api/v1/log/stream')
  source.onopen = () => {
    connected.value = true
    error.value = ''
  }
  source.onmessage = (ev) => {
    try {
      const e = JSON.parse(ev.data)
      if (paused.value) pending.unshift(e)
      else push([e])
    } catch {
      /* ignore malformed */
    }
  }
  source.onerror = () => {
    connected.value = false
    // The browser retries on its own; a 503/401 will keep failing, so say so.
    if (!error.value) error.value = 'Stream disconnected; retrying…'
  }
}

async function load() {
  try {
    const recent = await api.log.recent(200)
    entries.value = recent.reverse()
    error.value = ''
    connect()
  } catch (e) {
    if (e instanceof ApiError && e.status === 503)
      error.value =
        'The firewall log needs the daemon to run as root (it is unavailable in this session).'
    else error.value = e instanceof Error ? e.message : String(e)
  }
}

function togglePause() {
  paused.value = !paused.value
  if (!paused.value && pending.length) {
    push(pending)
    pending = []
  }
}

function clear() {
  entries.value = []
  pending = []
}

const visible = computed(() => {
  const q = filter.value.trim().toLowerCase()
  if (!q) return entries.value
  return entries.value.filter((e) => JSON.stringify(e).toLowerCase().includes(q))
})

function label(e) {
  if (e.kind === 'rule') return e.ruleId
  if (e.kind === 'zone-drop') return `${e.zone} default`
  if (e.kind === 'default-drop') return 'default drop'
  return e.prefix || '—'
}

function endpoint(addr, port) {
  if (!addr) return ''
  return port ? `${addr}:${port}` : addr
}

onMounted(load)
onBeforeUnmount(() => source?.close())
</script>

<template>
  <div class="space-y-3">
    <div class="flex flex-wrap items-center gap-2">
      <button type="button" class="btn-secondary" @click="togglePause">
        <component :is="paused ? Play : Pause" class="mr-1 size-4" aria-hidden="true" />
        {{ paused ? 'Resume' : 'Pause' }}
      </button>
      <button type="button" class="btn-secondary" @click="clear">
        <Trash2 class="mr-1 size-4" aria-hidden="true" /> Clear
      </button>
      <input
        v-model="filter"
        class="input max-w-xs"
        placeholder="Filter (address, port, rule, interface…)"
        aria-label="Filter log"
      />
      <span
        class="ml-auto text-xs"
        :class="connected ? 'text-emerald-700 dark:text-emerald-300' : 'text-neutral-500'"
      >
        {{ connected ? 'live' : 'not connected' }} · {{ visible.length }} shown
      </span>
    </div>
    <p class="text-xs text-neutral-500">
      Rules with logging on, zones with "log drops", and the default-drop logging under System all
      appear here. Nothing is written to the kernel log.
    </p>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Matched</th>
            <th>In</th>
            <th>Out</th>
            <th>Proto</th>
            <th>Source</th>
            <th>Destination</th>
            <th>Info</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!visible.length">
            <td colspan="8" class="text-neutral-500">Nothing logged yet.</td>
          </tr>
          <tr v-for="(e, i) in visible" :key="e.time + i">
            <td class="font-mono text-xs whitespace-nowrap">
              {{ new Date(e.time).toLocaleTimeString() }}
            </td>
            <td>
              <span
                class="badge"
                :class="{ 'badge-ok': e.kind === 'rule', 'badge-warn': e.kind !== 'rule' }"
                >{{ label(e) }}</span
              >
            </td>
            <td class="font-mono text-xs">{{ e.in }}</td>
            <td class="font-mono text-xs">{{ e.out }}</td>
            <td class="font-mono text-xs">{{ e.proto }}</td>
            <td class="font-mono text-xs">{{ endpoint(e.src, e.srcPort) }}</td>
            <td class="font-mono text-xs">{{ endpoint(e.dst, e.dstPort) }}</td>
            <td class="font-mono text-xs text-neutral-500">
              <template v-if="e.tcpFlags">{{ e.tcpFlags }}</template>
              <template v-else-if="e.proto?.startsWith('icmp')">type {{ e.icmpType }}</template>
              <template v-else>{{ e.length }} B</template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
