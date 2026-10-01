<script setup>
import { ChevronDown, ChevronUp } from 'lucide-vue-next'
import { computed } from 'vue'

/**
 * A column header that sorts its table: a click sorts by it, a second
 * reverses. The chevron and aria-sort say which way. Over a cell of two
 * figures, such as a rate and a total, a click moves to the next figure
 * and the header reads its label, holding the longest label's width so
 * the table stays put.
 */
const props = defineProps({
  /** The column's key in the sort's columns. */
  by: { type: String, default: '' },
  /** Or the columns a click moves through, as [key, label] pairs. */
  columns: { type: Array, default: null },
  /** What useSort returned. */
  sort: { type: Object, required: true },
})
const keys = computed(() => (props.columns ? props.columns.map(([key]) => key) : [props.by]))
/** The key sorted by, if it is this header's. */
const current = computed(() => {
  const by = props.sort.order.value.by
  return keys.value.includes(by) ? by : ''
})
const dir = computed(() => (current.value ? props.sort.order.value.dir : ''))
const shown = computed(() => current.value || keys.value[0])
</script>

<template>
  <th
    :aria-sort="dir ? (dir === 'asc' ? 'ascending' : 'descending') : undefined"
    class="aria-[sort]:text-ink"
  >
    <!-- The button takes the header's alignment, so a label shorter than
         the longest sits against the chevron. -->
    <button
      type="button"
      class="inline-flex items-center gap-1 rounded-sm [text-align:inherit] uppercase hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none max-sm:-my-3 max-sm:min-h-11"
      @click="sort.toggle(keys)"
    >
      <span v-if="columns" class="inline-grid whitespace-nowrap">
        <span
          v-for="[key, label] in columns"
          :key="key"
          class="col-start-1 row-start-1"
          :class="{ invisible: key !== shown }"
        >
          {{ label }}
        </span>
      </span>
      <slot v-else />
      <ChevronUp v-if="dir === 'asc'" class="size-3.5" aria-hidden="true" />
      <ChevronDown v-else-if="dir === 'desc'" class="size-3.5" aria-hidden="true" />
    </button>
  </th>
</template>
