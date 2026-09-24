<script setup>
import { X } from 'lucide-vue-next'
import {
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from 'reka-ui'
import { computed } from 'vue'

import { provideLocked } from '@/lib/locked'
import { useAuthStore } from '@/stores/auth'

const props = defineProps({
  title: { type: String, required: true },
  description: { type: String, default: '' },
  /**
   * Shows the form without letting it be sent: its fields disabled and
   * one button that closes it. Unset, it follows the account, so a viewer
   * reads every dialog this way.
   */
  readOnly: { type: Boolean, default: undefined },
})
const open = defineModel('open', { type: Boolean, default: false })

const auth = useAuthStore()
const locked = computed(() => props.readOnly ?? auth.readOnly)
provideLocked(() => locked.value)
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay
        class="fixed inset-0 z-40 bg-black/40 backdrop-blur-[1px] data-[state=closed]:animate-fade-out data-[state=open]:animate-fade-in motion-reduce:animate-none"
      />
      <DialogContent
        class="fixed top-1/2 left-1/2 z-50 max-h-[90dvh] w-[min(40rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-xl border border-line bg-surface p-6 shadow-xl focus:outline-none data-[state=closed]:animate-pop-out data-[state=open]:animate-pop-in motion-reduce:animate-none max-sm:top-auto max-sm:bottom-0 max-sm:left-0 max-sm:max-h-[85dvh] max-sm:w-full max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-b-none max-sm:border-x-0 max-sm:border-b-0 max-sm:px-4 max-sm:pt-4 max-sm:pb-[max(1rem,env(safe-area-inset-bottom))] max-sm:[--pop-from-opacity:1] max-sm:[--pop-from:translateY(100%)]"
      >
        <div class="mb-4 flex items-start justify-between gap-4">
          <div>
            <DialogTitle class="text-lg font-semibold tracking-tight">{{ title }}</DialogTitle>
            <DialogDescription v-if="description" class="mt-1 text-sm text-ink-muted">
              {{ description }}
            </DialogDescription>
          </div>
          <DialogClose
            class="rounded-md p-1 text-ink-muted hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none max-sm:-mt-2.5 max-sm:-mr-2.5 max-sm:inline-flex max-sm:min-h-11 max-sm:min-w-11 max-sm:items-center max-sm:justify-center"
            aria-label="Close"
          >
            <X class="size-4" aria-hidden="true" />
          </DialogClose>
        </div>
        <template v-if="locked">
          <fieldset disabled class="read-only min-w-0">
            <slot />
          </fieldset>
          <div class="mt-4 flex justify-end">
            <DialogClose class="btn-secondary">Close</DialogClose>
          </div>
        </template>
        <slot v-else />
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
