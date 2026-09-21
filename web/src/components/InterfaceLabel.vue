<script setup>
/**
 * An interface as a person knows it: the description first when there is
 * one, its zone, and the kernel name in mono so the two never get mixed up.
 */
defineProps({
  iface: { type: Object, required: true },
})
</script>

<template>
  <span class="inline-flex flex-wrap items-baseline gap-x-2">
    <span :class="iface.description ? '' : 'font-mono'">{{ iface.description || iface.name }}</span>
    <span v-if="iface.zone" class="badge">{{ iface.zone }}</span>
    <span v-if="iface.description" class="font-mono text-code text-ink-muted">{{
      iface.name
    }}</span>
  </span>
</template>
