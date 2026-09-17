<script setup>
import { RefreshCw } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

/**
 * The one refresh button. Disabled and spinning while its loader runs,
 * and says how long ago it last succeeded, which is what makes a page
 * that polls read as live.
 */
const props = defineProps({
  busy: { type: Boolean, default: false },
  /** Epoch milliseconds of the last success; 0 hides the age. */
  updatedAt: { type: Number, default: 0 },
  label: { type: String, default: 'Refresh' },
  busyLabel: { type: String, default: 'Refreshing…' },
  /** Off for a reason other than being busy, e.g. nothing to check. */
  disabled: { type: Boolean, default: false },
})
defineEmits(['click'])

const now = ref(Date.now())
let tick = 0
onMounted(() => {
  tick = window.setInterval(() => (now.value = Date.now()), 1000)
})
onBeforeUnmount(() => window.clearInterval(tick))

const age = computed(() => {
  if (!props.updatedAt) return ''
  const s = Math.max(0, Math.round((now.value - props.updatedAt) / 1000))
  if (s < 5) return 'just now'
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  return `${Math.floor(m / 60)}h ago`
})
</script>

<template>
  <span class="inline-flex items-center gap-2">
    <button
      type="button"
      class="btn-secondary"
      :disabled="busy || disabled"
      :aria-busy="busy"
      @click="$emit('click')"
    >
      <RefreshCw class="mr-1 size-4" :class="{ 'animate-spin': busy }" aria-hidden="true" />
      {{ busy ? busyLabel : label }}
    </button>
    <span v-if="age" class="text-xs text-neutral-500 tabular-nums">{{ age }}</span>
  </span>
</template>
