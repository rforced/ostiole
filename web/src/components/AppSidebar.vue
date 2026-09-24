<script setup>
import { LogOut, Shield } from 'lucide-vue-next'
import { computed } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import ThemeToggle from '@/components/ThemeToggle.vue'
import { NAV } from '@/lib/nav'
import { ROLE_LABELS, useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

/**
 * The brand, the nav tree and the account: a column beside the page on a
 * wide screen, the body of the drawer on a narrow one. The drawer puts its
 * close button in the slot at the end of the brand row.
 */
defineProps({
  /** Fills the drawer rather than standing beside the page. */
  drawer: { type: Boolean, default: false },
})
const emit = defineEmits(['navigate'])

const auth = useAuthStore()
const config = useConfigStore()
const confirm = useConfirmStore()
const route = useRoute()
const router = useRouter()

/** An item shows its pages while you are somewhere inside it. */
const inside = (item) => route.path === item.to || route.path.startsWith(`${item.to}/`)

/** Which router this is, under the brand, from the applied configuration. */
const hostname = computed(() => config.saved?.system?.hostname ?? '')
const initial = computed(() => (auth.user?.username ?? '?').slice(0, 1))

/**
 * A viewer changes nothing, so a draft that differs for one is a page
 * filling in a default, not an edit worth a dot or a question.
 */
const dirty = computed(() => config.dirty && !auth.readOnly)
const changed = (to) => !auth.readOnly && config.hasChanges(to)

/** Signing out drops the draft, so a dirty one is worth a question. */
async function logout() {
  if (
    dirty.value &&
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
</script>

<template>
  <aside
    :class="
      drawer
        ? 'flex h-full flex-col'
        : 'sticky top-0 flex h-dvh w-60 shrink-0 flex-col border-r border-line'
    "
  >
    <div class="flex items-center gap-2.5 px-4 pt-4 pb-3">
      <Shield class="size-6 shrink-0 text-accent" aria-hidden="true" />
      <div class="min-w-0 leading-tight">
        <div class="font-semibold tracking-tight">Ostiole</div>
        <div v-if="hostname" class="truncate font-mono text-xs text-ink-muted">
          {{ hostname }}
        </div>
      </div>
      <slot />
    </div>
    <nav class="flex flex-1 flex-col gap-0.5 overflow-y-auto px-3 py-2" aria-label="Main">
      <!-- The dot describes a link rather than renaming it, so "Interfaces"
           is still "Interfaces" to a screen reader and to the tests. -->
      <span id="nav-unapplied" class="sr-only">has unapplied changes</span>
      <template v-for="item in NAV" :key="item.to">
        <RouterLink
          :to="item.to"
          class="flex items-center gap-2.5 rounded-md px-2.5 py-2 text-[0.9375rem] text-ink-2 hover:bg-surface-2 hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none max-lg:min-h-11 [&>svg]:text-ink-muted"
          active-class="bg-surface-2 font-medium text-ink [&>svg]:text-accent"
          exact-active-class=""
          :aria-describedby="changed(item.to) ? 'nav-unapplied' : undefined"
          @click="emit('navigate')"
        >
          <component :is="item.icon" class="size-[18px] shrink-0" aria-hidden="true" />
          {{ item.label }}
          <span
            v-if="changed(item.to)"
            class="ml-auto size-2 rounded-full bg-warn"
            title="Unapplied changes"
            aria-hidden="true"
          />
        </RouterLink>
        <RouterLink
          v-for="page in inside(item) ? (item.pages ?? []) : []"
          :key="page.path"
          :to="`${item.to}/${page.path}`"
          class="ml-[1.35rem] flex items-center gap-2 border-l border-line py-1.5 pl-4 text-sm text-ink-muted hover:border-line-2 hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none max-lg:min-h-11"
          active-class="border-accent font-medium text-ink"
          :aria-describedby="changed(`${item.to}/${page.path}`) ? 'nav-unapplied' : undefined"
          @click="emit('navigate')"
        >
          {{ page.label }}
          <span
            v-if="changed(`${item.to}/${page.path}`)"
            class="ml-auto size-2 rounded-full bg-warn"
            title="Unapplied changes"
            aria-hidden="true"
          />
        </RouterLink>
      </template>
    </nav>
    <div
      class="space-y-2 border-t border-line p-3 max-lg:pb-[max(0.75rem,env(safe-area-inset-bottom))]"
    >
      <div class="flex items-center gap-2.5 px-1">
        <span
          class="flex size-7 shrink-0 items-center justify-center rounded-full bg-surface-2 text-xs font-semibold text-ink-2 uppercase"
          aria-hidden="true"
        >
          {{ initial }}
        </span>
        <span v-if="auth.user" class="min-w-0 flex-1">
          <span class="block truncate text-sm font-medium">{{ auth.user.username }}</span>
          <span v-if="ROLE_LABELS[auth.user.role]" class="block truncate text-xs text-ink-muted">
            {{ ROLE_LABELS[auth.user.role] }}
          </span>
        </span>
        <button
          type="button"
          class="icon-btn max-lg:min-h-11 max-lg:min-w-11 max-lg:items-center max-lg:justify-center"
          title="Sign out"
          aria-label="Sign out"
          @click="logout"
        >
          <LogOut class="size-4" aria-hidden="true" />
        </button>
      </div>
      <ThemeToggle block />
    </div>
  </aside>
</template>
