<script setup>
import { AlertTriangle } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import ApplyPending from '@/components/ApplyPending.vue'
import { ApiError, api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import { useSystemStore } from '@/stores/system'

const CONFIRM_SECONDS = 60

const config = useConfigStore()
const system = useSystemStore()

const busy = ref(false)
const error = ref('')
const issues = ref([])
const pending = ref(null)

const visible = computed(() => config.dirty || pending.value !== null)

watch(
  () => config.dirty,
  () => {
    error.value = ''
    issues.value = []
  },
)

async function apply() {
  busy.value = true
  error.value = ''
  issues.value = []
  try {
    await api.config.check(config.draft)
    pending.value = await api.config.apply(config.draft, CONFIRM_SECONDS)
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
  await system.refresh()
  window.setTimeout(() => (pending.value = null), LINGER_MS)
}
</script>

<template>
  <div
    v-if="visible"
    class="border-b border-neutral-200 bg-neutral-50 px-6 py-3 dark:border-neutral-800 dark:bg-neutral-900"
  >
    <ApplyPending
      v-if="pending"
      :deadline="pending.deadline"
      @confirmed="confirmed"
      @reverted="reverted"
    />
    <div v-else class="space-y-2">
      <div class="flex flex-wrap items-center gap-3 text-sm">
        <AlertTriangle class="size-4 text-amber-600 dark:text-amber-400" aria-hidden="true" />
        <span class="font-medium">Unapplied changes.</span>
        <span class="text-neutral-500"
          >Nothing reaches the kernel until you apply and confirm.</span
        >
        <div class="ml-auto flex gap-2">
          <button type="button" class="btn-secondary" :disabled="busy" @click="config.discard()">
            Discard
          </button>
          <button type="button" class="btn-primary" :disabled="busy" @click="apply">
            {{ busy ? 'Applying…' : `Apply with ${CONFIRM_SECONDS}s confirmation` }}
          </button>
        </div>
      </div>
      <div v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">
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
</template>
