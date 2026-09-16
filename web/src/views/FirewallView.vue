<script setup>
import { onMounted } from 'vue'
import { useRoute } from 'vue-router'

import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const route = useRoute()
onMounted(() => config.load())
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">{{ route.meta.title }}</h1>
    <p v-if="config.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ config.error }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-neutral-500">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>
    <RouterView v-else-if="config.draft" />
  </div>
</template>
