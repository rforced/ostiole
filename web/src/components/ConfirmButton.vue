<script setup>
import { LoaderCircle } from '@lucide/vue'

import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'

/**
 * A destructive action behind the shared confirm dialog. Emits `confirm`
 * only after the admin says yes there. A viewer changes nothing, so it is
 * not there for one. Disabled and spinning while the action runs.
 */
const props = defineProps({
  label: { type: String, required: true },
  /** The dialog title, e.g. "Delete rule 12?" */
  question: { type: String, required: true },
  description: { type: String, default: '' },
  /** The yes button; defaults to the label. */
  confirmLabel: { type: String, default: '' },
  /** What goes with it, listed in the dialog. */
  dependents: { type: Array, default: () => [] },
  dependentsLabel: { type: String, default: 'Also deleted' },
  /** A name the admin has to type back first. */
  typed: { type: String, default: '' },
  danger: { type: Boolean, default: true },
  busy: { type: Boolean, default: false },
  /** The label while busy; empty keeps the label. */
  busyLabel: { type: String, default: '' },
  /** Off for a reason other than being busy, e.g. the role. */
  disabled: { type: Boolean, default: false },
})
const emit = defineEmits(['confirm'])
const auth = useAuthStore()
const confirm = useConfirmStore()

async function click() {
  const ok = await confirm.ask({
    question: props.question,
    description: props.description,
    confirmLabel: props.confirmLabel || props.label,
    dependents: props.dependents,
    dependentsLabel: props.dependentsLabel,
    typed: props.typed,
    danger: props.danger,
  })
  if (ok) emit('confirm')
}
</script>

<template>
  <button
    v-if="!auth.readOnly"
    type="button"
    class="link-action enabled:hover:text-bad"
    :disabled="busy || disabled"
    :aria-busy="busy"
    @click="click"
  >
    <LoaderCircle v-if="busy" class="mr-1 inline size-4 animate-spin" aria-hidden="true" />
    {{ busy && busyLabel ? busyLabel : label }}
  </button>
</template>
