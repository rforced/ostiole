<script setup>
import { X } from 'lucide-vue-next'

import { useToastStore } from '@/stores/toast'

const toast = useToastStore()
</script>

<template>
  <div
    class="pointer-events-none fixed right-4 bottom-4 z-50 flex w-80 flex-col gap-2"
    aria-live="polite"
  >
    <TransitionGroup name="toast">
      <div
        v-for="t in toast.toasts"
        :key="t.id"
        class="pointer-events-auto flex items-center gap-3 rounded-lg border border-neutral-200 bg-white p-3 text-sm shadow-lg dark:border-neutral-700 dark:bg-neutral-900"
      >
        <span class="min-w-0 flex-1">{{ t.message }}</span>
        <button v-if="t.action" type="button" class="link font-medium" @click="toast.act(t.id)">
          {{ t.action.label }}
        </button>
        <button type="button" class="icon-btn" aria-label="Dismiss" @click="toast.dismiss(t.id)">
          <X class="size-4" aria-hidden="true" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>
