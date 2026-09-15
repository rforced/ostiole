<script setup>
import { Activity, Cog, LogOut, Network, Route, Server, Shield } from 'lucide-vue-next'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'

import ThemeToggle from '@/components/ThemeToggle.vue'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const nav = [
  { to: '/', label: 'Dashboard', icon: Activity },
  { to: '/interfaces', label: 'Interfaces', icon: Network },
  { to: '/firewall', label: 'Firewall', icon: Shield },
  { to: '/routing', label: 'Routing', icon: Route },
  { to: '/services', label: 'Services', icon: Server },
  { to: '/system', label: 'System', icon: Cog },
]

async function logout() {
  await auth.logout()
  router.push({ name: 'login' })
}
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
        <RouterLink
          v-for="item in nav"
          :key="item.to"
          :to="item.to"
          class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm text-neutral-600 hover:bg-neutral-200/60 hover:text-neutral-900 dark:text-neutral-400 dark:hover:bg-neutral-800 dark:hover:text-neutral-100"
          active-class="bg-neutral-200/60 font-medium text-neutral-900 dark:bg-neutral-800 dark:text-neutral-100"
          exact-active-class=""
        >
          <component :is="item.icon" class="size-4" aria-hidden="true" />
          {{ item.label }}
        </RouterLink>
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
      <main class="flex-1 p-6">
        <RouterView />
      </main>
    </div>
  </div>
</template>
