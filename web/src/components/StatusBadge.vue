<script setup>
import { computed } from 'vue'

/**
 * The word for what a thing is doing, coloured by whether that is fine.
 * Services say running, stopped or off; links up or down; tunnels
 * connected or disconnected; the clock synchronised or not, and each time
 * server in use, agrees, standby, no answer, wrong time or unsteady.
 */
const props = defineProps({
  state: { type: String, required: true },
})

const OK = new Set([
  'running',
  'up',
  'connected',
  'loaded',
  'healthy',
  'active',
  'synchronised',
  'in use',
  'agrees',
])
const WARN = new Set([
  'stopped',
  'no carrier',
  'absent',
  'disconnected',
  'inactive',
  'missing',
  'not synchronised',
  'no answer',
  'unsteady',
])
const BAD = new Set(['failed', 'failing', 'error', 'wrong time'])

const tone = computed(() => {
  if (OK.has(props.state)) return 'badge-ok'
  if (WARN.has(props.state)) return 'badge-warn'
  if (BAD.has(props.state)) return 'badge-bad'
  return ''
})
</script>

<template>
  <span class="badge" :class="tone">{{ state }}</span>
</template>
