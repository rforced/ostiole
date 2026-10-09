<script setup>
import { Search } from '@lucide/vue'
import { useId } from 'vue'

import { formatCount } from '@/lib/format'

/**
 * The one search box: the list narrows as you type, whether the page holds
 * it or the router searches it. Given a total, it says how many rows are
 * left while it holds a query; the card's count says it otherwise.
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
    <span v-if="total !== null && query.trim()" class="text-ink-muted tabular-nums"
      >{{ formatCount(shown) }} of {{ formatCount(total) }}</span
    >
  </div>
</template>
