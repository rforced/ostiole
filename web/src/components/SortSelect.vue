<script setup>
/**
 * The sort of a table whose header row a phone hides: each column in the
 * order its first click would give. Only below sm; above it the headers
 * sort.
 */
defineProps({
  /** What useSort returned. */
  sort: { type: Object, required: true },
  /** The columns offered, as [key, label] pairs in header order. */
  columns: { type: Array, required: true },
})
</script>

<template>
  <select
    class="input w-auto sm:hidden max-sm:w-full"
    aria-label="Sort"
    :value="sort.order.value.by ?? ''"
    :disabled="sort.locked.value"
    @change="sort.choose($event.target.value)"
  >
    <option v-if="!sort.order.value.by" value="" disabled>Sort</option>
    <option v-for="[key, label] in columns" :key="key" :value="key">{{ label }}</option>
  </select>
</template>
