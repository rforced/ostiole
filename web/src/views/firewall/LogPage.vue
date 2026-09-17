<script setup>
import { Pause, Play, Trash2 } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import { ApiError, api } from '@/lib/api'
import { useAsync } from '@/lib/async'

const MAX_ROWS = 500

const entries = ref([])
const paused = ref(false)
const filter = ref('')
const streamError = ref('')
const connected = ref(false)
let source = null
let pending = []
let seq = 0

/** Newest first, each row keyed once so the table can animate it in and out. */
function push(list) {
  const keyed = list.map((e) => ({ ...e, key: ++seq }))
  entries.value = [...keyed, ...entries.value].slice(0, MAX_ROWS)
}

function connect() {
  // EventSource sends the session cookie on same-origin requests.
  source = new EventSource('/api/v1/log/stream')
  source.onopen = () => {
    connected.value = true
    streamError.value = ''
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
    if (!streamError.value) streamError.value = 'Stream disconnected, retrying…'
  }
}

const load = useAsync(async () => {
  let recent
  try {
    recent = await api.log.recent(200)
  } catch (e) {
    if (e instanceof ApiError && e.status === 503)
      throw new Error('The firewall log needs the daemon to run as root.', { cause: e })
    throw e
  }
  push(recent.reverse())
  connect()
})

const error = computed(() => load.error.value || streamError.value)

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
  // The row key is ours, not the packet's, so it is not searched.
  return entries.value.filter((e) =>
    JSON.stringify(e, (k, v) => (k === 'key' ? undefined : v))
      .toLowerCase()
      .includes(q),
  )
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

onMounted(load.run)
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
    <p class="text-sm text-neutral-500">
      Only rules with logging on, zones that log drops, and the default-drop log under System appear
      here.
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
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!visible.length" key="empty" class="row-static">
            <td colspan="8" class="text-neutral-500">
              {{
                load.busy.value
                  ? 'Reading the log…'
                  : entries.length
                    ? `Nothing matches "${filter.trim()}".`
                    : 'Nothing logged yet.'
              }}
            </td>
          </tr>
          <tr v-for="e in visible" :key="e.key">
            <td class="font-mono text-code whitespace-nowrap">
              {{ new Date(e.time).toLocaleTimeString() }}
            </td>
            <td>
              <span
                class="badge"
                :class="{ 'badge-ok': e.kind === 'rule', 'badge-warn': e.kind !== 'rule' }"
                >{{ label(e) }}</span
              >
            </td>
            <td class="font-mono text-code">{{ e.in }}</td>
            <td class="font-mono text-code">{{ e.out }}</td>
            <td class="font-mono text-code">{{ e.proto }}</td>
            <td class="font-mono text-code">{{ endpoint(e.src, e.srcPort) }}</td>
            <td class="font-mono text-code">{{ endpoint(e.dst, e.dstPort) }}</td>
            <td class="font-mono text-code text-neutral-500">
              <template v-if="e.tcpFlags">{{ e.tcpFlags }}</template>
              <template v-else-if="e.proto?.startsWith('icmp')">type {{ e.icmpType }}</template>
              <template v-else>{{ e.length }} B</template>
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
  </div>
</template>
