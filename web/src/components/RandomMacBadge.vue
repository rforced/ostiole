<script setup>
import { isRandomMAC } from '@/lib/mac'

/**
 * Beside a device's MAC wherever one is shown, when the device made it up,
 * as phones and laptops do for a private address per network. Never
 * beside the router's own: its bridges and VLANs make theirs up by design.
 */
defineProps({
  mac: { type: String, default: '' },
})
</script>

<template>
  <span
    v-if="isRandomMAC(mac)"
    class="badge ml-1 font-sans"
    title="The device may change this address."
    >random</span
  >
</template>
