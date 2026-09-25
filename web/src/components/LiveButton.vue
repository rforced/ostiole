<script setup>
import { computed } from 'vue'

/**
 * The one Live switch, for a list that keeps itself current. On, new rows
 * arrive as they happen; off, the table holds still. Its dot is green and
 * ringed while live, amber while the stream or the reads are failing.
 */
const props = defineProps({
  /** The stream is down or the last read failed. */
  failing: { type: Boolean, default: false },
  disabled: { type: Boolean, default: false },
})
const live = defineModel({ type: Boolean, default: false })
const state = computed(() => {
  if (!live.value) return 'off'
  return props.failing ? 'failing' : 'live'
})
</script>

<template>
  <button
    type="button"
    class="group btn-secondary"
    :aria-pressed="live"
    :data-state="state"
    :disabled="disabled"
    @click="live = !live"
  >
    <span class="relative flex size-2.5" aria-hidden="true">
      <span
        class="absolute hidden size-full rounded-full bg-ok opacity-60 group-data-[state=live]:inline-flex group-data-[state=live]:animate-live motion-reduce:animate-none!"
      ></span>
      <span
        class="relative inline-flex size-2.5 rounded-full bg-ink-faint group-data-[state=failing]:bg-warn group-data-[state=live]:bg-ok"
      ></span>
    </span>
    Live
  </button>
</template>
