<script setup>
import { Pause, Play, Trash2 } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import SectionCard from '@/components/SectionCard.vue'
import { ApiError, api } from '@/lib/api'
import { useAsync } from '@/lib/async'

const MAX_ROWS = 500

const entries = ref([])
const paused = ref(false)
const filter = ref('')
// Blocked by default: an accept rule left logging on a busy zone would
// otherwise bury the refusals anyone opens this page to find.
const show = ref('blocked')
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
  push(recent)
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

/**
 * Blocked covers drop and reject. A packet logged by a ruleset written
 * before the verdict went into the prefix has no action to go on, so it
 * shows either way rather than being hidden on a guess.
 */
function chosen(e) {
  if (show.value === 'all' || !e.action) return true
  return show.value === 'blocked' ? e.action !== 'accept' : e.action === 'accept'
}

const visible = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return entries.value.filter((e) => {
    if (!chosen(e)) return false
    if (!q) return true
    // The row key is ours, not the packet's, so it is not searched.
    return JSON.stringify(e, (k, v) => (k === 'key' ? undefined : v))
      .toLowerCase()
      .includes(q)
  })
})

function label(e) {
  if (e.kind === 'rule') return e.ruleId
  if (e.kind === 'zone-drop') return `${e.zone} default`
  if (e.kind === 'default-drop') return 'default drop'
  if (e.kind === 'block-private') return 'private source'
  if (e.kind === 'block-bogons') return 'bogon source'
  return e.prefix || '—'
}

/** The verdict carries the colour: that a rule matched says nothing on
 *  its own about whether the packet got through. */
function actionClass(action) {
  if (action === 'accept') return 'badge-ok'
  if (action === 'reject') return 'badge-warn'
  if (action === 'drop') return 'badge-bad'
  return ''
}

function endpoint(addr, port) {
  if (!addr) return ''
  return port ? `${addr}:${port}` : addr
}

onMounted(load.run)
onBeforeUnmount(() => source?.close())
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Log"
      intro="Only rules with logging on, zones that log drops, and the default-drop log under
        System appear here."
      flush
    >
      <template #actions>
        <button type="button" class="btn-secondary" @click="togglePause">
          <component :is="paused ? Play : Pause" class="size-4" aria-hidden="true" />
          {{ paused ? 'Resume' : 'Pause' }}
        </button>
        <button type="button" class="btn-secondary" @click="clear">
          <Trash2 class="size-4" aria-hidden="true" /> Clear
        </button>
      </template>
      <div class="flex flex-wrap items-center gap-3 px-4 py-3">
        <input
          v-model="filter"
          class="input max-w-xs"
          placeholder="Filter (address, port, rule, interface…)"
          aria-label="Filter log"
        />
        <select v-model="show" class="input w-36" aria-label="Show">
          <option value="blocked">Blocked</option>
          <option value="allowed">Allowed</option>
          <option value="all">All</option>
        </select>
        <span class="text-xs" :class="connected ? 'text-ok' : 'text-ink-muted'">
          {{ connected ? 'live' : 'not connected' }} · {{ visible.length }} shown
        </span>
        <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
      </div>

      <table class="table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Action</th>
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
            <td colspan="9" class="text-ink-muted">
              {{
                load.busy.value
                  ? 'Reading…'
                  : entries.length
                    ? `Nothing matches ${filter.trim() ? `"${filter.trim()}"` : 'this view'}.`
                    : 'No packets logged.'
              }}
            </td>
          </tr>
          <tr v-for="e in visible" :key="e.key">
            <td class="font-mono text-code whitespace-nowrap">
              {{ new Date(e.time).toLocaleTimeString() }}
            </td>
            <td>
              <span class="badge" :class="actionClass(e.action)">{{ e.action || 'unknown' }}</span>
            </td>
            <td>
              <span class="badge">{{ label(e) }}</span>
            </td>
            <td class="font-mono text-code">{{ e.in }}</td>
            <td class="font-mono text-code">{{ e.out }}</td>
            <td class="font-mono text-code">{{ e.proto }}</td>
            <td class="font-mono text-code">{{ endpoint(e.src, e.srcPort) }}</td>
            <td class="font-mono text-code">{{ endpoint(e.dst, e.dstPort) }}</td>
            <td class="font-mono text-code text-ink-muted">
              <template v-if="e.tcpFlags">{{ e.tcpFlags }}</template>
              <template v-else-if="e.proto?.startsWith('icmp')">type {{ e.icmpType }}</template>
              <template v-else>{{ e.length }} B</template>
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>
  </div>
</template>
