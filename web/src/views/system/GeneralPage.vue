<script setup>
import FetchedAliasesSection from '@/views/system/FetchedAliasesSection.vue'
import LogsSection from '@/views/system/LogsSection.vue'
import ManagementSection from '@/views/system/ManagementSection.vue'
</script>

<template>
  <div class="space-y-5">
    <ManagementSection />
    <FetchedAliasesSection />
    <LogsSection />
  </div>
</template>
