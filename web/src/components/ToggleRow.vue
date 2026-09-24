<script setup>
import { computed, useId } from 'vue'

/**
 * A checkbox with its label beside it and a hint under. As a switch it is
 * the one flag that governs a card or a page, and sits label first so it
 * reads right at the end of a header.
 */
const props = defineProps({
  label: { type: String, required: true },
  hint: { type: String, default: '' },
  variant: {
    type: String,
    default: 'checkbox',
    validator: (v) => ['checkbox', 'switch'].includes(v),
  },
  disabled: { type: Boolean, default: false },
  id: { type: String, default: '' },
  /** For a screen reader when the visible label is only "Enabled". */
  ariaLabel: { type: String, default: '' },
})
const checked = defineModel({ type: Boolean, default: false })

const uid = useId()
const inputId = computed(() => props.id || uid)
const isSwitch = computed(() => props.variant === 'switch')
</script>

<template>
  <div class="flex items-start gap-2.5 text-sm" :class="{ 'flex-row-reverse': isSwitch }">
    <input
      :id="inputId"
      v-model="checked"
      type="checkbox"
      :role="isSwitch ? 'switch' : undefined"
      :class="isSwitch ? 'switch' : 'mt-0.5 size-4 shrink-0 rounded'"
      :disabled="disabled"
      :aria-label="ariaLabel || undefined"
    />
    <label :for="inputId" class="min-w-0" :class="{ 'flex-1': !isSwitch, 'opacity-60': disabled }">
      {{ label }}
      <span v-if="hint" class="block text-ink-muted">{{ hint }}</span>
    </label>
  </div>
</template>
