<script setup>
import { LogOut, Shield } from 'lucide-vue-next'
import { onBeforeUnmount, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'

import ApplyBar from '@/components/ApplyBar.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ThemeToggle from '@/components/ThemeToggle.vue'
import ToastStack from '@/components/ToastStack.vue'
import { NAV } from '@/lib/nav'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

const auth = useAuthStore()
const config = useConfigStore()
const confirm = useConfirmStore()
const route = useRoute()
const router = useRouter()

/** An item shows its pages while you are somewhere inside it. */
const inside = (item) => route.path === item.to || route.path.startsWith(`${item.to}/`)

/** Signing out drops the draft, so a dirty one is worth a question. */
async function logout() {
  if (
    config.dirty &&
    !(await confirm.ask({
      question: 'Sign out with unapplied changes?',
      description: 'The draft is kept only in this tab. Signing out throws it away.',
      confirmLabel: 'Sign out',
    }))
  )
    return
  await auth.logout()
  router.push({ name: 'login' })
}

// The browser asks before a reload or close would lose the draft.
function keep(e) {
  e.preventDefault()
}
watch(
  () => config.dirty,
  (dirty) => {
    if (dirty) window.addEventListener('beforeunload', keep)
    else window.removeEventListener('beforeunload', keep)
  },
)
onBeforeUnmount(() => window.removeEventListener('beforeunload', keep))
</script>

<template>
  <RouterView v-if="route.meta.public" />
  <div v-else class="flex min-h-screen">
    <aside
      class="flex w-56 shrink-0 flex-col border-r border-neutral-200 bg-neutral-50 dark:border-neutral-800 dark:bg-neutral-900"
    >
      <div class="flex h-14 items-center gap-2 px-4 font-semibold tracking-tight">
        <Shield class="size-5 text-sky-600 dark:text-sky-400" aria-hidden="true" />
        Ostiole
      </div>
      <nav class="flex flex-1 flex-col gap-0.5 px-2" aria-label="Main">
        <!-- The dot describes a link rather than renaming it, so "Interfaces"
             is still "Interfaces" to a screen reader and to the tests. -->
        <span id="nav-unapplied" class="sr-only">has unapplied changes</span>
        <template v-for="item in NAV" :key="item.to">
          <RouterLink
            :to="item.to"
            class="flex items-center gap-2.5 rounded-md px-2.5 py-2.5 text-[0.9375rem] text-neutral-600 hover:bg-neutral-200/60 hover:text-neutral-900 dark:text-neutral-400 dark:hover:bg-neutral-800 dark:hover:text-neutral-100"
            active-class="bg-neutral-200/60 font-medium text-neutral-900 dark:bg-neutral-800 dark:text-neutral-100"
            exact-active-class=""
            :aria-describedby="config.hasChanges(item.to) ? 'nav-unapplied' : undefined"
          >
            <component :is="item.icon" class="size-5" aria-hidden="true" />
            {{ item.label }}
            <span
              v-if="config.hasChanges(item.to)"
              class="ml-auto size-2 rounded-full bg-amber-500"
              title="Unapplied changes"
              aria-hidden="true"
            />
          </RouterLink>
          <RouterLink
            v-for="page in inside(item) ? (item.pages ?? []) : []"
            :key="page.path"
            :to="`${item.to}/${page.path}`"
            class="ml-4 flex items-center gap-2 border-l border-neutral-200 py-2 pl-4 text-[0.9375rem] text-neutral-500 hover:border-neutral-400 hover:text-neutral-900 dark:border-neutral-800 dark:text-neutral-400 dark:hover:border-neutral-600 dark:hover:text-neutral-100"
            active-class="border-sky-600 font-medium text-neutral-900 dark:border-sky-400 dark:text-neutral-100"
            :aria-describedby="
              config.hasChanges(`${item.to}/${page.path}`) ? 'nav-unapplied' : undefined
            "
          >
            {{ page.label }}
            <span
              v-if="config.hasChanges(`${item.to}/${page.path}`)"
              class="ml-auto size-2 rounded-full bg-amber-500"
              title="Unapplied changes"
              aria-hidden="true"
            />
          </RouterLink>
        </template>
      </nav>
    </aside>

    <div class="flex min-w-0 flex-1 flex-col">
      <header
        class="flex h-14 items-center justify-end gap-3 border-b border-neutral-200 px-4 dark:border-neutral-800"
      >
        <span v-if="auth.user" class="text-sm text-neutral-500">{{ auth.user.username }}</span>
        <ThemeToggle />
        <button
          type="button"
          class="rounded-md p-1.5 text-neutral-500 hover:text-neutral-900 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:outline-none dark:hover:text-neutral-100"
          title="Sign out"
          aria-label="Sign out"
          @click="logout"
        >
          <LogOut class="size-4" aria-hidden="true" />
        </button>
      </header>
      <ApplyBar />
      <main class="flex-1 p-6">
        <RouterView />
      </main>
    </div>
    <ConfirmDialog />
    <ToastStack />
  </div>
</template>
