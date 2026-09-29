<script setup>
import { computed } from 'vue'
import { useRoute } from 'vue-router'

/**
 * The top of every page: eyebrow, title, one line under it, and the
 * page's actions on the right. Title and eyebrow come from the nav tree
 * unless the page says otherwise.
 */
const props = defineProps({
  title: { type: String, default: '' },
  eyebrow: { type: String, default: '' },
  intro: { type: String, default: '' },
})

const route = useRoute()
const title = computed(() => props.title || route?.meta.title || '')
const eyebrow = computed(() => props.eyebrow || route?.meta.section?.label || '')
const intro = computed(() => props.intro || route?.meta.section?.intro || '')
</script>

<template>
  <header class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
    <div class="min-w-0">
      <p v-if="eyebrow" class="eyebrow">{{ eyebrow }}</p>
      <div class="flex flex-wrap items-center gap-3">
        <h1 class="page-title">{{ title }}</h1>
        <slot name="status" />
      </div>
      <p v-if="intro" class="mt-1 max-w-3xl text-sm text-ink-muted">{{ intro }}</p>
    </div>
    <div v-if="$slots.default" class="flex flex-wrap items-center gap-2">
      <slot />
    </div>
  </header>
</template>
