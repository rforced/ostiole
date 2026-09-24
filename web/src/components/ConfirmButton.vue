<script setup>
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'

/**
 * A destructive action behind the shared confirm dialog. Emits `confirm`
 * only after the admin says yes there. A viewer changes nothing, so it is
 * not there for one.
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
  <button v-if="!auth.readOnly" type="button" class="link-action hover:text-bad" @click="click">
    {{ label }}
  </button>
</template>
