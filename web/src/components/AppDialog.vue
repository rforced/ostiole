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

defineProps({
  title: { type: String, required: true },
  description: { type: String, default: '' },
})
const open = defineModel('open', { type: Boolean, default: false })
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay
        class="fixed inset-0 z-40 bg-black/40 backdrop-blur-[1px] data-[state=closed]:animate-fade-out data-[state=open]:animate-fade-in motion-reduce:animate-none"
      />
      <DialogContent
        class="fixed top-1/2 left-1/2 z-50 max-h-[90vh] w-[min(40rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-xl border border-neutral-200 bg-white p-6 shadow-xl focus:outline-none data-[state=closed]:animate-pop-out data-[state=open]:animate-pop-in motion-reduce:animate-none dark:border-neutral-800 dark:bg-neutral-900"
      >
        <div class="mb-4 flex items-start justify-between gap-4">
          <div>
            <DialogTitle class="text-lg font-semibold tracking-tight">{{ title }}</DialogTitle>
            <DialogDescription v-if="description" class="mt-1 text-sm text-neutral-500">
              {{ description }}
            </DialogDescription>
          </div>
          <DialogClose
            class="rounded-md p-1 text-neutral-500 hover:text-neutral-900 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:outline-none dark:hover:text-neutral-100"
            aria-label="Close"
          >
            <X class="size-4" aria-hidden="true" />
          </DialogClose>
        </div>
        <slot />
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
