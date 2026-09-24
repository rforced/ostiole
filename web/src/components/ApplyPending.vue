<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import { ApiError, api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'

/**
 * Shown while an apply awaits confirmation. Counts down to the deadline,
 * lets the admin confirm or revert, and notices when the apply stopped
 * pending without a click here. The countdown is a timer, which a screen
 * reader leaves alone; the outcome is what the live region announces.
 */
const props = defineProps({
  /** ISO timestamp the server will revert at. */
  deadline: { type: String, required: true },
  /**
   * Works out what happened to an apply that stopped pending without a
   * click here: 'confirmed', 'reverted' or 'expired'. Without it the apply
   * is taken to have run out.
   */
  settle: { type: Function, default: null },
})
const emit = defineEmits(['confirmed', 'reverted'])

const auth = useAuthStore()
const system = useSystemStore()
const now = ref(Date.now())
/** The request in flight: 'confirm', 'revert' or ''. */
const busy = ref('')
const error = ref('')
const outcome = ref('')

const remaining = computed(() =>
  Math.max(0, Math.round((Date.parse(props.deadline) - now.value) / 1000)),
)

let tick = 0
let poll = 0
let settling = false

/** The apply is no longer pending and nothing here ended it. */
async function ended() {
  if (settling) return
  settling = true
  const result = props.settle ? await props.settle() : 'expired'
  if (outcome.value) return
  outcome.value = result
  emit(result === 'confirmed' ? 'confirmed' : 'reverted')
}

onMounted(() => {
  tick = window.setInterval(() => {
    now.value = Date.now()
  }, 250)
  poll = window.setInterval(async () => {
    const status = await system.refresh()
    if (status && !status.pending && !outcome.value && !busy.value) await ended()
  }, 2000)
})
onBeforeUnmount(() => {
  window.clearInterval(tick)
  window.clearInterval(poll)
})

async function confirm() {
  busy.value = 'confirm'
  error.value = ''
  try {
    await api.config.confirm()
    outcome.value = 'confirmed'
    await system.refresh()
    emit('confirmed')
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) await ended()
    else error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = ''
  }
}

async function revert() {
  busy.value = 'revert'
  error.value = ''
  try {
    await api.config.revert()
    outcome.value = 'reverted'
    await system.refresh()
    emit('reverted')
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) await ended()
    else error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <div
    class="rounded-lg border border-warn-line bg-warn-soft p-4 text-sm text-warn-ink"
    role="status"
    aria-live="polite"
  >
    <template v-if="!outcome && auth.readOnly">
      <p class="font-medium">Changes applied, awaiting confirmation.</p>
      <p class="mt-1">
        Unless an operator confirms them within
        <span role="timer" aria-live="off" class="font-mono font-semibold tabular-nums"
          >{{ remaining }}s</span
        >, the previous configuration is restored.
      </p>
    </template>
    <template v-else-if="!outcome">
      <p class="font-medium">Changes applied, awaiting confirmation.</p>
      <p class="mt-1">
        If this page can still reach the firewall, confirm within
        <span role="timer" aria-live="off" class="font-mono font-semibold tabular-nums"
          >{{ remaining }}s</span
        >. Otherwise the previous configuration is restored automatically.
      </p>
      <div class="mt-3 flex gap-2">
        <button
          type="button"
          class="btn-primary"
          :disabled="busy !== ''"
          :aria-busy="busy === 'confirm'"
          @click="confirm"
        >
          <LoaderCircle v-if="busy === 'confirm'" class="size-4 animate-spin" aria-hidden="true" />
          {{ busy === 'confirm' ? 'Confirming…' : 'Confirm' }}
        </button>
        <button
          type="button"
          class="btn-secondary"
          :disabled="busy !== ''"
          :aria-busy="busy === 'revert'"
          @click="revert"
        >
          <LoaderCircle v-if="busy === 'revert'" class="size-4 animate-spin" aria-hidden="true" />
          {{ busy === 'revert' ? 'Reverting…' : 'Revert now' }}
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
