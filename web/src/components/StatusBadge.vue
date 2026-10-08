<script setup>
import { computed } from 'vue'

import { tone } from '@/lib/badge'

/** The word for what a thing is doing, coloured by whether that is fine. */
const props = defineProps({
  state: { type: String, required: true },
})

const toneClass = computed(() => tone(props.state))
</script>

<template>
  <span class="badge" :class="toneClass">{{ state }}</span>
</template>
