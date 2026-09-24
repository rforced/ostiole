<script setup>
import { computed, nextTick, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfirmStore } from '@/stores/confirm'

/**
 * Renders whatever the confirm store is asking. Focus lands on Cancel, or
 * on the field when a name has to be typed, so Enter never confirms by
 * accident.
 */
const confirm = useConfirmStore()
const typed = ref('')
const input = ref(null)
const cancel = ref(null)

const SHOWN = 12

const open = computed({
  get: () => confirm.request !== null,
  set: (v) => {
    if (!v) confirm.settle(false)
  },
})

const req = computed(() => confirm.request)
const shown = computed(() => (req.value?.dependents ?? []).slice(0, SHOWN))
const hidden = computed(() => (req.value?.dependents.length ?? 0) - shown.value.length)
const ready = computed(() => !req.value?.typed || typed.value.trim() === req.value.typed)

watch(req, async (r) => {
  typed.value = ''
  if (!r) return
  await nextTick()
  ;(input.value ?? cancel.value)?.focus()
})

function submit() {
  if (ready.value) confirm.settle(true)
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="req?.question ?? ''"
    :description="req?.description ?? ''"
    :read-only="false"
  >
    <form v-if="req" class="space-y-4" @submit.prevent="submit">
      <div v-if="req.dependents.length" class="text-sm">
        <p class="mb-1 text-ink-muted">{{ req.dependentsLabel }}</p>
        <ul class="list-disc pl-5">
          <li v-for="d in shown" :key="d">{{ d }}</li>
          <li v-if="hidden > 0" class="text-ink-muted">and {{ hidden }} more</li>
        </ul>
      </div>
      <FormField v-if="req.typed" id="confirm-typed" :label="`Type ${req.typed} to confirm`">
        <input
          id="confirm-typed"
          ref="input"
          v-model="typed"
          class="input font-mono"
          autocomplete="off"
          spellcheck="false"
        />
      </FormField>
      <div class="flex justify-end gap-2">
        <button ref="cancel" type="button" class="btn-secondary" @click="confirm.settle(false)">
          Cancel
        </button>
        <button type="submit" :class="req.danger ? 'btn-danger' : 'btn-primary'" :disabled="!ready">
          {{ req.confirmLabel }}
        </button>
      </div>
    </form>
  </AppDialog>
</template>
