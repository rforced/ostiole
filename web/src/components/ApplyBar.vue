<script setup>
import { AlertTriangle } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import ApplyPending from '@/components/ApplyPending.vue'
import ChangeList from '@/components/ChangeList.vue'
import { ApiError, api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import { useSystemStore } from '@/stores/system'

const CONFIRM_SECONDS = 60
const SHOWN_CHANGES = 20

const config = useConfigStore()
const system = useSystemStore()

const busy = ref(false)
const error = ref('')
const issues = ref([])
const pending = ref(null)
const showChanges = ref(false)

const visible = computed(() => config.dirty || pending.value !== null)
const count = computed(() => config.changes.length)

watch(
  () => config.dirty,
  (dirty) => {
    error.value = ''
    issues.value = []
    if (!dirty) showChanges.value = false
  },
)

async function apply() {
  busy.value = true
  error.value = ''
  issues.value = []
  try {
    await api.config.check(config.draft)
    pending.value = await api.config.apply(config.draft, CONFIRM_SECONDS)
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

const LINGER_MS = 2500

async function confirmed() {
  config.markSaved()
  await system.refresh()
  // Leave the outcome on screen briefly before the bar goes away.
  window.setTimeout(() => (pending.value = null), LINGER_MS)
}

async function reverted() {
  // Keep the draft so the admin can fix it and try again.
  config.markApplied()
  await system.refresh()
  window.setTimeout(() => (pending.value = null), LINGER_MS)
}
</script>

<template>
  <Transition name="bar">
    <div v-if="visible" class="sticky top-0 z-30 grid grid-rows-[1fr]">
      <div class="min-h-0 overflow-hidden">
        <div class="border-b border-line bg-surface px-6 py-3">
          <ApplyPending
            v-if="pending"
            :deadline="pending.deadline"
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
                class="link"
                :aria-expanded="showChanges"
                @click="showChanges = !showChanges"
              >
                {{ showChanges ? 'Hide' : 'Show' }} {{ count }}
                {{ count === 1 ? 'change' : 'changes' }}
              </button>
              <span class="text-ink-muted"
                >Nothing reaches the kernel until you apply and confirm.</span
              >
              <div class="ml-auto flex gap-2">
                <button
                  type="button"
                  class="btn-secondary"
                  :disabled="busy"
                  @click="config.discard()"
                >
                  Discard
                </button>
                <button type="button" class="btn-primary" :disabled="busy" @click="apply">
                  {{ busy ? 'Applying…' : `Apply with ${CONFIRM_SECONDS}s confirmation` }}
                </button>
              </div>
            </div>
            <ChangeList v-if="showChanges" :changes="config.changes" :limit="SHOWN_CHANGES" />
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
