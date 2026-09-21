<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import { ApiError, api } from '@/lib/api'
import { useSystemStore } from '@/stores/system'

/**
 * Shown while an apply awaits confirmation. Counts down to the deadline,
 * lets the admin confirm or revert, and notices when the server reverted
 * on its own.
 */
const props = defineProps({
  /** ISO timestamp the server will revert at. */
  deadline: { type: String, required: true },
})
const emit = defineEmits(['confirmed', 'reverted'])

const system = useSystemStore()
const now = ref(Date.now())
const busy = ref(false)
const error = ref('')
const outcome = ref('')

const remaining = computed(() =>
  Math.max(0, Math.round((Date.parse(props.deadline) - now.value) / 1000)),
)

let tick = 0
let poll = 0
onMounted(() => {
  tick = window.setInterval(() => {
    now.value = Date.now()
  }, 250)
  poll = window.setInterval(async () => {
    const status = await system.refresh()
    if (status && !status.pending && !outcome.value) {
      outcome.value = 'expired'
      emit('reverted')
    }
  }, 2000)
})
onBeforeUnmount(() => {
  window.clearInterval(tick)
  window.clearInterval(poll)
})

async function confirm() {
  busy.value = true
  error.value = ''
  try {
    await api.config.confirm()
    outcome.value = 'confirmed'
    await system.refresh()
    emit('confirmed')
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      outcome.value = 'expired'
      emit('reverted')
    } else error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function revert() {
  busy.value = true
  error.value = ''
  try {
    await api.config.revert()
    outcome.value = 'reverted'
    await system.refresh()
    emit('reverted')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div
    class="rounded-lg border border-warn-line bg-warn-soft p-4 text-sm text-warn-ink"
    role="status"
    aria-live="polite"
  >
    <template v-if="!outcome">
      <p class="font-medium">Changes applied, awaiting confirmation.</p>
      <p class="mt-1">
        If this page can still reach the firewall, confirm within
        <span class="font-mono font-semibold tabular-nums">{{ remaining }}s</span>. Otherwise the
        previous configuration is restored automatically.
      </p>
      <div class="mt-3 flex gap-2">
        <button type="button" class="btn-primary" :disabled="busy" @click="confirm">Confirm</button>
        <button type="button" class="btn-secondary" :disabled="busy" @click="revert">
          Revert now
        </button>
      </div>
    </template>
    <p v-else-if="outcome === 'confirmed'" class="font-medium text-ok">Confirmed and saved.</p>
    <p v-else-if="outcome === 'reverted'" class="font-medium">
      Reverted to the previous configuration.
    </p>
    <p v-else class="font-medium">
      Not confirmed in time. The previous configuration was restored.
    </p>
    <p v-if="error" role="alert" class="mt-2 text-bad">{{ error }}</p>
  </div>
</template>
