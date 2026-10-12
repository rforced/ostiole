<script setup>
import { ChevronUp } from '@lucide/vue'
import {
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuPortal,
  DropdownMenuRoot,
  DropdownMenuTrigger,
} from 'reka-ui'
import { computed, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'

import { ROLE_LABELS, useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

/** The signed-in account at the foot of the sidebar, and what to do with it. */
defineOptions({ inheritAttrs: false })
const emit = defineEmits(['navigate'])

const auth = useAuthStore()
const config = useConfigStore()
const confirm = useConfirmStore()
const router = useRouter()

const initial = computed(() => (auth.user?.username ?? '?').slice(0, 1))

/**
 * A viewer changes nothing, so a draft that differs for one is a page
 * filling in a default, not an edit worth a question.
 */
const dirty = computed(() => config.dirty && !auth.readOnly)

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

const trigger = ref(null)
// An action runs once the menu has closed, with focus back on the trigger,
// as MenuButton does: the confirm returns focus to whatever had it.
let chosen = null

function choose(run) {
  chosen = run
}

function closed(event) {
  if (!chosen) return
  event.preventDefault()
  trigger.value?.focus()
  const run = chosen
  chosen = null
  run()
}

const ITEM =
  'flex cursor-default items-center rounded px-2.5 py-1.5 text-sm text-ink outline-none select-none data-highlighted:bg-accent data-highlighted:text-on-fill max-lg:min-h-11'
</script>

<template>
  <DropdownMenuRoot>
    <DropdownMenuTrigger as-child>
      <button
        ref="trigger"
        type="button"
        aria-label="Account menu"
        v-bind="$attrs"
        class="flex w-full cursor-pointer items-center gap-2.5 rounded-md px-1 py-1 text-left hover:bg-surface-2 focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none aria-expanded:bg-surface-2 max-lg:min-h-11"
      >
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
        <ChevronUp class="ml-auto size-4 shrink-0 text-ink-muted" aria-hidden="true" />
      </button>
    </DropdownMenuTrigger>
    <DropdownMenuPortal>
      <DropdownMenuContent
        side="top"
        align="start"
        :side-offset="4"
        :collision-padding="8"
        class="z-50 min-w-(--reka-dropdown-menu-trigger-width) rounded-md border border-line bg-surface p-1 shadow-lg focus:outline-none"
        @close-auto-focus="closed"
      >
        <DropdownMenuItem as-child @select="emit('navigate')">
          <RouterLink to="/account" :class="ITEM">Account</RouterLink>
        </DropdownMenuItem>
        <DropdownMenuItem :class="ITEM" @select="choose(logout)">Sign out</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
</template>
