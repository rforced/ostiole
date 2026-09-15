<script setup>
defineProps({
  title: { type: String, required: true },
})
</script>

<template>
  <div class="space-y-2">
    <h1 class="text-2xl font-semibold tracking-tight">{{ title }}</h1>
    <p class="text-sm text-neutral-500">
      Not implemented yet. DHCP and DNS (dnsmasq and unbound, driven by Ostiole) are planned here.
    </p>
  </div>
</template>
