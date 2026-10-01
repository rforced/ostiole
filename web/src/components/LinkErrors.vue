<script setup>
import { computed } from 'vue'

import { formatCount } from '@/lib/format'

/**
 * A link's errors in the last 72 hours, under its state wherever links are
 * listed. Nothing when it had none: older errors age out on the router, so
 * there is nothing to clear.
 */
const props = defineProps({
  /** A link from GET /overview or GET /interfaces/live; none when absent. */
  link: { type: Object, default: null },
})

// "72 hours" stays on one line where the Link column is narrow.
const words = computed(() => {
  const n = (props.link?.rxErrors ?? 0) + (props.link?.txErrors ?? 0)
  return n ? `${formatCount(n)} error${n === 1 ? '' : 's'} in the last 72\u00a0hours` : ''
})
</script>

<template>
  <div v-if="words" class="mt-1 text-warn">{{ words }}</div>
</template>
