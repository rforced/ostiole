<script setup>
import { ChevronDown, ChevronUp } from 'lucide-vue-next'
import { computed } from 'vue'

/**
 * A column header that sorts its table: a click sorts by it, a second
 * reverses. The chevron and aria-sort say which way. While the order is
 * locked, as Live locks it, the header is only a header.
 */
const props = defineProps({
  /** The column's key in the sort's columns. */
  by: { type: String, required: true },
  /** What useSort returned. */
  sort: { type: Object, required: true },
})
const dir = computed(() => {
  const order = props.sort.order.value
  return order.by === props.by ? order.dir : ''
})
</script>

<template>
  <th
    :aria-sort="dir ? (dir === 'asc' ? 'ascending' : 'descending') : undefined"
    class="aria-[sort]:text-ink"
  >
    <button
      type="button"
      class="inline-flex items-center gap-1 rounded-sm uppercase enabled:hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none disabled:cursor-default max-sm:-my-3 max-sm:min-h-11"
      :disabled="sort.locked.value"
      @click="sort.toggle(by)"
    >
      <slot />
      <ChevronUp v-if="dir === 'asc'" class="size-3.5" aria-hidden="true" />
      <ChevronDown v-else-if="dir === 'desc'" class="size-3.5" aria-hidden="true" />
    </button>
  </th>
</template>
