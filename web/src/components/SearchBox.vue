<script setup>
import { Search } from 'lucide-vue-next'
import { useId } from 'vue'

/**
 * The one search box, for a list the page already holds: the list narrows
 * as you type. Given a total, it says how many rows are left.
 */
defineProps({
  /** What it matches, e.g. "address, MAC, or interface". */
  placeholder: { type: String, required: true },
  /** Rows left after the search. */
  shown: { type: Number, default: 0 },
  /** Rows in all; null hides the count. */
  total: { type: Number, default: null },
})
const query = defineModel({ type: String, default: '' })
const id = useId()
</script>

<template>
  <div class="flex flex-wrap items-center gap-3 max-sm:w-full">
    <div class="relative w-80 max-sm:w-full">
      <label :for="id" class="sr-only">Search</label>
      <Search
        class="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-ink-faint"
        aria-hidden="true"
      />
      <input
        :id="id"
        v-model="query"
        type="search"
        class="input pl-8"
        :placeholder="placeholder"
        spellcheck="false"
        autocomplete="off"
      />
    </div>
    <span v-if="total !== null" class="text-ink-muted tabular-nums"
      >{{ shown }} of {{ total }}</span
    >
  </div>
</template>
