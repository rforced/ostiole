<script setup>
import { useConfirmStore } from '@/stores/confirm'

/**
 * A destructive action behind the shared confirm dialog. Emits `confirm`
 * only after the admin says yes there.
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
})
const emit = defineEmits(['confirm'])
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
    type="button"
    class="text-sm text-neutral-500 underline-offset-2 hover:text-red-600 hover:underline focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:outline-none dark:hover:text-red-400"
    @click="click"
  >
    {{ label }}
  </button>
</template>
