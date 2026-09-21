<script setup>
import { Shield } from 'lucide-vue-next'

import ThemeToggle from '@/components/ThemeToggle.vue'

defineProps({
  title: { type: String, required: true },
  subtitle: { type: String, default: '' },
})
</script>

<template>
  <div class="flex min-h-screen flex-col items-center justify-center p-6">
    <div class="absolute top-4 right-4">
      <ThemeToggle />
    </div>
    <div class="w-full max-w-sm rounded-xl border border-line bg-surface p-6 shadow-sm">
      <div class="mb-6 flex items-center gap-2">
        <Shield class="size-6 text-accent-text" aria-hidden="true" />
        <div>
          <h1 class="text-lg font-semibold tracking-tight">{{ title }}</h1>
          <p v-if="subtitle" class="text-sm text-ink-muted">{{ subtitle }}</p>
        </div>
      </div>
      <slot />
    </div>
  </div>
</template>
