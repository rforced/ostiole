<script setup>
import AppNotice from '@/components/AppNotice.vue'

defineProps({
  /** @type {import('vue').PropType<Array<{kind: string, level: string, title: string, detail?: string}>>} */
  warnings: { type: Array, default: () => [] },
})
</script>

<template>
  <section v-if="warnings.length" aria-label="Warnings" class="space-y-2">
    <AppNotice
      v-for="w in warnings"
      :key="w.kind + w.title"
      :data-warning="w.kind"
      :kind="w.level === 'info' ? 'info' : 'warn'"
      :title="w.title"
    >
      <template v-if="w.detail">{{ w.detail }}</template>
    </AppNotice>
  </section>
</template>
