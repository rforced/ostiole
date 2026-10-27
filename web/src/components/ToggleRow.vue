<script setup lang="ts" generic="T extends boolean | string | number | undefined">
import { computed, useId } from 'vue'

/**
 * A checkbox or a radio with its label beside it and a hint under. As a
 * switch it is the one flag that governs a card or a page, and sits label
 * first so it reads right at the end of a header. A radio's model is the
 * value its group has chosen, and its hint describes the choice rather
 * than naming it.
 */
const props = withDefaults(
  defineProps<{
    label: string
    hint?: string
    variant?: 'checkbox' | 'switch' | 'radio'
    disabled?: boolean
    id?: string
    /** For a screen reader when the visible label is only "Enabled". */
    ariaLabel?: string
    /** A radio's group, and the value it stands for. */
    name?: string
    value?: T
  }>(),
  {
    hint: '',
    variant: 'checkbox',
    disabled: false,
    id: '',
    ariaLabel: '',
    name: '',
    value: undefined,
  },
)
const model = defineModel<T>({ required: true })

function choose() {
  if (props.value !== undefined) model.value = props.value
}

const uid = useId()
const inputId = computed(() => props.id || uid)
const isSwitch = computed(() => props.variant === 'switch')
const isRadio = computed(() => props.variant === 'radio')
</script>

<template>
  <div class="flex items-start gap-2.5 text-sm" :class="{ 'flex-row-reverse': isSwitch }">
    <input
      v-if="isRadio"
      :id="inputId"
      type="radio"
      class="checkbox mt-0.5"
      :name="name || undefined"
      :value="value"
      :checked="model === value"
      :disabled="disabled"
      :aria-describedby="hint || $slots.hint ? `${inputId}-hint` : undefined"
      @change="choose"
    />
    <input
      v-else
      :id="inputId"
      v-model="model"
      type="checkbox"
      :role="isSwitch ? 'switch' : undefined"
      :class="isSwitch ? 'switch' : 'checkbox mt-0.5'"
      :disabled="disabled"
      :aria-label="ariaLabel || undefined"
    />
    <!-- On a phone the label reaches past its line, so a short one is still
         a thumb's height to tap, without moving anything. -->
    <div v-if="isRadio" class="min-w-0 flex-1" :class="{ 'opacity-60': disabled }">
      <label :for="inputId" class="block max-sm:-my-3 max-sm:py-3">{{ label }}</label>
      <p v-if="hint || $slots.hint" :id="`${inputId}-hint`" class="text-ink-muted">
        <slot name="hint">{{ hint }}</slot>
      </p>
    </div>
    <label
      v-else
      :for="inputId"
      class="min-w-0 max-sm:-my-3 max-sm:py-3"
      :class="{ 'flex-1': !isSwitch, 'opacity-60': disabled }"
    >
      {{ label }}
      <span v-if="hint || $slots.hint" class="block text-ink-muted">
        <slot name="hint">{{ hint }}</slot>
      </span>
    </label>
  </div>
</template>
