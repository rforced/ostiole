<script setup>
import { ref } from 'vue'

/** A destructive action that asks once, inline, before firing. */
defineProps({
  label: { type: String, required: true },
  confirmLabel: { type: String, default: 'Really?' },
})
const emit = defineEmits(['confirm'])
const armed = ref(false)

function click() {
  if (!armed.value) {
    armed.value = true
    window.setTimeout(() => (armed.value = false), 3000)
    return
  }
  armed.value = false
  emit('confirm')
}
</script>

<template>
  <button
    type="button"
    class="text-sm underline-offset-2 hover:underline"
    :class="armed ? 'font-medium text-red-600 dark:text-red-400' : 'text-neutral-500'"
    @click="click"
  >
    {{ armed ? confirmLabel : label }}
  </button>
</template>
