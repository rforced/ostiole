<script setup>
import { computed, onBeforeUnmount, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'

import AppSidebar from '@/components/AppSidebar.vue'
import AppTopBar from '@/components/AppTopBar.vue'
import ApplyBar from '@/components/ApplyBar.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ToastStack from '@/components/ToastStack.vue'
import { useNarrow } from '@/lib/media'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()
const route = useRoute()

/**
 * Only one of the sidebar and the top bar is mounted, so the nav tree is
 * in the page once. The page itself stays mounted across the switch.
 */
const narrow = useNarrow()

// The sidebar reads the hostname, so the configuration loads with the shell.
watch(
  () => auth.loggedIn,
  (on) => {
    if (on) config.load()
  },
  { immediate: true },
)

/** A viewer's draft differs only where a page filled in a default. */
const dirty = computed(() => config.dirty && !auth.readOnly)

// The browser asks before a reload or close would lose the draft.
function keep(e) {
  e.preventDefault()
}
watch(
  () => dirty.value,
  (on) => {
    if (on) window.addEventListener('beforeunload', keep)
    else window.removeEventListener('beforeunload', keep)
  },
)
onBeforeUnmount(() => window.removeEventListener('beforeunload', keep))
</script>

<template>
  <RouterView v-if="route.meta.public" />
  <div v-else class="flex min-h-dvh max-lg:flex-col">
    <AppTopBar v-if="narrow" />
    <AppSidebar v-else />

    <div class="flex min-w-0 flex-1 flex-col">
      <ApplyBar />
      <!-- Below lg the apply bar is docked over the bottom of the page, and
           --dock is its height, so the end of the page scrolls clear of it. -->
      <main
        class="flex-1 p-6 max-lg:px-[max(1.5rem,env(safe-area-inset-left),env(safe-area-inset-right))] max-lg:pb-[calc(max(var(--dock,0px),env(safe-area-inset-bottom))+1.5rem)] max-sm:px-4 max-sm:pt-4 max-sm:pb-[calc(max(var(--dock,0px),env(safe-area-inset-bottom))+1rem)]"
      >
        <RouterView />
      </main>
    </div>
    <ConfirmDialog />
    <ToastStack />
  </div>
</template>
