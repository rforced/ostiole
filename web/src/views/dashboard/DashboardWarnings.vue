<script setup>
import AppNotice from '@/components/AppNotice.vue'

defineProps({
  /** @type {import('vue').PropType<Array<{kind: string, level: string, title: string, detail?: string}>>} */
  warnings: { type: Array, default: () => [] },
  /** False until the first overview arrives. */
  loaded: { type: Boolean, default: true },
  /** How many warnings to hold room for until then. */
  placeholders: { type: Number, default: 0 },
})
</script>

<template>
  <section v-if="loaded && warnings.length" aria-label="Warnings" class="space-y-2">
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
  <!-- A notice's box and its two lines, in no colour: a warning seen last
       time may be gone by now. -->
  <div v-else-if="!loaded && placeholders" class="space-y-2" aria-hidden="true" data-reading>
    <div
      v-for="n in placeholders"
      :key="n"
      class="flex gap-3 rounded-lg border border-line bg-surface p-3 text-sm"
    >
      <span class="skeleton mt-0.5 size-4 shrink-0 rounded-full"></span>
      <div class="min-w-0 space-y-0.5">
        <p><span class="skeleton w-48"></span></p>
        <div><span class="skeleton w-96 max-w-full"></span></div>
      </div>
    </div>
  </div>
</template>
