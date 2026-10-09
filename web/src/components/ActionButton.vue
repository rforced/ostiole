<script setup>
import { LoaderCircle } from '@lucide/vue'

/**
 * The button for an action that takes a while. Disabled while it runs,
 * its icon a spinner and its label the participle, "Sending…" for "Send a
 * test". As a link it is a row action.
 */
defineProps({
  label: { type: String, required: true },
  /** The label while busy; empty keeps the label. */
  busyLabel: { type: String, default: '' },
  busy: { type: Boolean, default: false },
  kind: {
    type: String,
    default: 'secondary',
    validator: (k) => ['primary', 'secondary', 'danger', 'link'].includes(k),
  },
  type: { type: String, default: 'button' },
  /** Off for a reason other than being busy. */
  disabled: { type: Boolean, default: false },
  /** Shown before the label while not busy. */
  icon: { type: [Object, Function], default: null },
})

const CLASS = {
  primary: 'btn-primary',
  secondary: 'btn-secondary',
  danger: 'btn-danger',
  link: 'link-action',
}
</script>

<template>
  <button :type="type" :class="CLASS[kind]" :disabled="busy || disabled" :aria-busy="busy">
    <LoaderCircle
      v-if="busy"
      class="size-4 animate-spin"
      :class="{ 'mr-1 inline': kind === 'link' }"
      aria-hidden="true"
    />
    <component :is="icon" v-else-if="icon" class="size-4" aria-hidden="true" />
    {{ busy && busyLabel ? busyLabel : label }}
  </button>
</template>
