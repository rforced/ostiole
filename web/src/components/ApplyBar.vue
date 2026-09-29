<script setup>
import { AlertTriangle, LoaderCircle } from 'lucide-vue-next'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import ApplyPending from '@/components/ApplyPending.vue'
import ChangeList from '@/components/ChangeList.vue'
import { ApiError, api } from '@/lib/api'
import { useNarrow } from '@/lib/media'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useSystemStore } from '@/stores/system'

const CONFIRM_SECONDS = 60
const SHOWN_CHANGES = 20
/** How long an outcome stays on screen before the bar goes away. */
const LINGER_MS = 2500

const auth = useAuthStore()
const config = useConfigStore()
const system = useSystemStore()
const route = useRoute()

const busy = ref(false)
const error = ref('')
const issues = ref([])
const showChanges = ref(false)
/**
 * The apply awaiting confirmation: one this tab made, or the server's,
 * whichever tab made it and across a reload, so the countdown is on every
 * page. Kept a moment after it settles so the outcome can be read.
 * @type {import('vue').Ref<{deadline: string} | null>}
 */
const pending = ref(null)
/** The draft as this tab applied it, until that apply settles. */
let applied = null

/** The wizard shows its own apply. */
const onWizard = computed(() => route?.name === 'wizard')
/** A viewer has nothing to apply, but sees an apply that is waiting. */
const visible = computed(
  () => !onWizard.value && ((config.dirty && !auth.readOnly) || pending.value !== null),
)
const count = computed(() => config.changes.length)

watch(
  [() => system.status?.pending?.deadline, onWizard],
  ([deadline, wizard]) => {
    if (deadline && !wizard && pending.value?.deadline !== deadline) pending.value = { deadline }
  },
  { immediate: true },
)

watch(
  () => config.dirty,
  (dirty) => {
    error.value = ''
    issues.value = []
    if (!dirty) showChanges.value = false
  },
)

/**
 * Below lg the bar is docked over the bottom of the page. --dock is how
 * much of the page it covers, so the page and the toasts can clear it.
 * Above lg nothing reads it, so nothing measures.
 */
const bar = ref(null)
const narrow = useNarrow()
let observer = null
function undock() {
  observer?.disconnect()
  observer = null
  document.documentElement.style.removeProperty('--dock')
}
watch(
  [bar, narrow],
  ([el, docked]) => {
    undock()
    if (!el || !docked || typeof ResizeObserver !== 'function') return
    observer = new ResizeObserver(() => {
      document.documentElement.style.setProperty('--dock', `${el.offsetHeight}px`)
    })
    observer.observe(el)
  },
  { flush: 'post' },
)
onBeforeUnmount(undock)

async function apply() {
  busy.value = true
  error.value = ''
  issues.value = []
  try {
    const draft = JSON.parse(JSON.stringify(config.draft))
    await api.config.check(draft)
    const res = await api.config.apply(draft, CONFIRM_SECONDS)
    applied = draft
    pending.value = { deadline: res.deadline }
    // The kernel changed the moment the apply returned, not when it is
    // confirmed, so anything showing live state is stale from here.
    config.markApplied()
    await system.refresh()
  } catch (e) {
    if (e instanceof ApiError) {
      error.value = e.message
      issues.value = e.issues
    } else error.value = String(e)
  } finally {
    busy.value = false
  }
}

function linger() {
  const shown = pending.value
  window.setTimeout(() => {
    if (pending.value === shown) pending.value = null
  }, LINGER_MS)
}

async function confirmed() {
  // Read the saved configuration back rather than assume it, so a confirm
  // for an apply made before a reload or in another tab updates this tab.
  if (!(await config.resync(applied)) && applied) config.markSaved(applied)
  applied = null
  linger()
}

async function reverted() {
  // Keep the draft so the admin can fix it and try again.
  applied = null
  config.markApplied()
  await system.refresh()
  linger()
}

/**
 * What happened to an apply that stopped pending without a click here.
 * Only a confirm writes the saved configuration, so a changed one means
 * it was confirmed somewhere else.
 */
async function settle() {
  const changed = await config.resync(applied)
  applied = null
  if (changed) return 'confirmed'
  return Date.now() >= Date.parse(pending.value?.deadline ?? '') ? 'expired' : 'reverted'
}
</script>

<template>
  <Transition name="bar">
    <div
      v-if="visible"
      ref="bar"
      class="sticky top-0 z-30 grid grid-rows-[1fr] max-lg:fixed max-lg:inset-x-0 max-lg:top-auto max-lg:bottom-0"
    >
      <div class="min-h-0 overflow-hidden">
        <div
          class="border-b border-line bg-surface px-6 py-3 max-lg:border-t max-lg:border-b-0 max-lg:px-[max(1.5rem,env(safe-area-inset-left),env(safe-area-inset-right))] max-lg:pb-[calc(0.75rem+env(safe-area-inset-bottom))] max-sm:px-4"
        >
          <ApplyPending
            v-if="pending"
            :key="pending.deadline"
            :deadline="pending.deadline"
            :settle="settle"
            @confirmed="confirmed"
            @reverted="reverted"
          />
          <div v-else class="space-y-2">
            <div class="flex flex-wrap items-center gap-3 text-sm">
              <AlertTriangle class="size-4 text-warn" aria-hidden="true" />
              <span class="font-medium">Unapplied changes.</span>
              <button
                v-if="count"
                type="button"
                class="link max-sm:-my-3 max-sm:py-3"
                :aria-expanded="showChanges"
                @click="showChanges = !showChanges"
              >
                {{ showChanges ? 'Hide' : 'Show' }} {{ count }}
                {{ count === 1 ? 'change' : 'changes' }}
              </button>
              <div class="ml-auto flex gap-2 max-sm:ml-0 max-sm:w-full">
                <button
                  type="button"
                  class="btn-secondary"
                  :disabled="busy"
                  @click="config.discard()"
                >
                  Discard
                </button>
                <button
                  type="button"
                  class="btn-primary max-sm:flex-1"
                  :disabled="busy"
                  :aria-busy="busy"
                  @click="apply"
                >
                  <LoaderCircle v-if="busy" class="size-4 animate-spin" aria-hidden="true" />
                  {{ busy ? 'Applying…' : `Apply with ${CONFIRM_SECONDS}s confirmation` }}
                </button>
              </div>
            </div>
            <ChangeList
              v-if="showChanges"
              :changes="config.changes"
              :limit="SHOWN_CHANGES"
              class="max-lg:max-h-[40dvh] max-lg:overflow-y-auto"
            />
            <div v-if="error" role="alert" class="text-sm text-bad">
              <p>{{ error }}</p>
              <ul v-if="issues.length" class="mt-1 list-disc pl-5">
                <li v-for="i in issues" :key="i.path + i.message">
                  <span class="font-mono">{{ i.path }}</span
                  >: {{ i.message }}
                </li>
              </ul>
            </div>
          </div>
        </div>
      </div>
    </div>
  </Transition>
</template>
