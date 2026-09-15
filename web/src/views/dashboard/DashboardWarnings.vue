<script setup>
import { AlertTriangle, Info } from 'lucide-vue-next'

defineProps({
  /** @type {import('vue').PropType<Array<{kind: string, level: string, title: string, detail?: string}>>} */
  warnings: { type: Array, default: () => [] },
})
</script>

<template>
  <section v-if="warnings.length" aria-label="Warnings" class="space-y-2">
    <div
      v-for="w in warnings"
      :key="w.kind + w.title"
      :data-warning="w.kind"
      class="flex gap-3 rounded-lg border p-3 text-sm"
      :class="
        w.level === 'info'
          ? 'border-sky-300 bg-sky-50 dark:border-sky-800 dark:bg-sky-950/40'
          : 'border-amber-300 bg-amber-50 dark:border-amber-800 dark:bg-amber-950/40'
      "
    >
      <component
        :is="w.level === 'info' ? Info : AlertTriangle"
        class="mt-0.5 size-4 shrink-0"
        :class="
          w.level === 'info'
            ? 'text-sky-600 dark:text-sky-400'
            : 'text-amber-600 dark:text-amber-400'
        "
        aria-hidden="true"
      />
      <div>
        <p class="font-medium">{{ w.title }}</p>
        <p v-if="w.detail" class="text-neutral-600 dark:text-neutral-400">{{ w.detail }}</p>
      </div>
    </div>
  </section>
</template>
