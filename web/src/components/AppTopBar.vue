<script setup>
import { Menu, Shield, X } from 'lucide-vue-next'
import {
  DialogClose,
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
  DialogTrigger,
  VisuallyHidden,
} from 'reka-ui'
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import AppSidebar from '@/components/AppSidebar.vue'
import { useConfigStore } from '@/stores/config'

/**
 * The shell on a narrow screen: a bar across the top, and the sidebar in a
 * drawer that its button opens. The drawer closes once you pick a page.
 */
const config = useConfigStore()
const route = useRoute()
const open = ref(false)

const hostname = computed(() => config.saved?.system?.hostname ?? '')

watch(
  () => route.fullPath,
  () => (open.value = false),
)
</script>

<template>
  <DialogRoot v-model:open="open">
    <header
      class="sticky top-0 z-30 flex items-center gap-2.5 border-b border-line bg-surface py-1.5 pr-[max(1rem,env(safe-area-inset-right))] pl-[max(0.5rem,env(safe-area-inset-left))]"
    >
      <DialogTrigger
        class="icon-btn min-h-11 min-w-11 items-center justify-center"
        aria-label="Open navigation"
      >
        <Menu class="size-5" aria-hidden="true" />
      </DialogTrigger>
      <Shield class="size-6 shrink-0 text-accent" aria-hidden="true" />
      <div class="min-w-0 leading-tight">
        <div class="font-semibold tracking-tight">Ostiole</div>
        <div v-if="hostname" class="truncate font-mono text-xs text-ink-muted">
          {{ hostname }}
        </div>
      </div>
    </header>
    <DialogPortal>
      <DialogOverlay
        class="fixed inset-0 z-40 bg-black/40 backdrop-blur-[1px] data-[state=closed]:animate-fade-out data-[state=open]:animate-fade-in motion-reduce:animate-none"
      />
      <DialogContent
        class="fixed inset-y-0 left-0 z-50 w-[min(18rem,85vw)] border-r border-line bg-surface pt-[env(safe-area-inset-top)] pl-[env(safe-area-inset-left)] shadow-xl focus:outline-none data-[state=closed]:animate-drawer-out data-[state=open]:animate-drawer-in motion-reduce:animate-none"
        :aria-describedby="undefined"
      >
        <VisuallyHidden>
          <DialogTitle>Navigation</DialogTitle>
        </VisuallyHidden>
        <AppSidebar drawer @navigate="open = false">
          <DialogClose
            class="icon-btn -mr-2 ml-auto min-h-11 min-w-11 items-center justify-center"
            aria-label="Close navigation"
          >
            <X class="size-5" aria-hidden="true" />
          </DialogClose>
        </AppSidebar>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
