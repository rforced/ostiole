<script setup>
import PageHeader from '@/components/PageHeader.vue'
</script>

<template>
  <div class="space-y-3">
    <PageHeader title="Not found" />
    <p class="text-sm text-ink-muted">
      That page does not exist.
      <RouterLink to="/" class="link">Back to dashboard</RouterLink>
    </p>
  </div>
</template>
