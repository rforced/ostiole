<script setup>
import { X } from 'lucide-vue-next'

import { useToastStore } from '@/stores/toast'

const toast = useToastStore()
</script>

<template>
  <div
    class="pointer-events-none fixed right-4 bottom-4 z-50 flex w-80 flex-col gap-2 max-lg:right-[max(1rem,env(safe-area-inset-right))] max-lg:bottom-[calc(max(var(--dock,0px),env(safe-area-inset-bottom))+1rem)] max-sm:left-4 max-sm:w-auto"
    aria-live="polite"
  >
    <TransitionGroup name="toast">
      <div
        v-for="t in toast.toasts"
        :key="t.id"
        class="pointer-events-auto flex items-center gap-3 rounded-lg border border-line bg-surface p-3 text-sm shadow-lg"
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
