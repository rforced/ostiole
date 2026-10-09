<script setup>
import { LoaderCircle } from '@lucide/vue'
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { shortTime } from '@/lib/log'

/**
 * The foot of a log's table. It reads the next page as it scrolls into
 * view, or on its button, and says where the reading stopped.
 */
const props = defineProps({
  /** Older entries remain to be read. */
  more: { type: Boolean, default: false },
  busy: { type: Boolean, default: false },
  /** How many rows the table holds. */
  rows: { type: Number, default: 0 },
  /** How far back a search looked when it stopped on its budget. */
  searchedTo: { type: String, default: '' },
})
const emit = defineEmits(['load'])

const foot = ref(null)
let observer = null

function inView(entries) {
  if (entries.some((e) => e.isIntersecting) && props.more && !props.busy) emit('load')
}

onMounted(() => {
  if (typeof IntersectionObserver === 'undefined' || !foot.value) return
  observer = new IntersectionObserver(inView)
  observer.observe(foot.value)
})

// A page that leaves the foot in view reads on: observing it again reports
// where it is now.
watch(
  () => props.busy,
  async (busy) => {
    if (busy || !observer || !foot.value) return
    await nextTick()
    observer.unobserve(foot.value)
    observer.observe(foot.value)
  },
)

onBeforeUnmount(() => observer?.disconnect())
</script>

<template>
  <div ref="foot" class="card-strip-row border-t border-line">
    <button
      v-if="more"
      type="button"
      class="btn-secondary"
      :disabled="busy"
      :aria-busy="busy"
      @click="emit('load')"
    >
      <LoaderCircle v-if="busy" class="size-4 animate-spin" aria-hidden="true" />
      Load more
    </button>
    <span v-if="searchedTo" class="text-sm text-ink-muted">
      Searched back to {{ shortTime(searchedTo) }}.
    </span>
    <span v-else-if="!more && rows" class="text-sm text-ink-muted">Start of the log.</span>
  </div>
</template>
