<script setup>
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'

import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const route = useRoute()
const section = computed(() => route.meta.section)

/** A page that edits the draft has nothing to show until the wizard has made one. */
const blocked = computed(() => route.meta.needsConfig && config.loaded && !config.draft)

onMounted(() => config.load())
</script>

<template>
  <div class="space-y-4">
    <div>
      <p class="text-xs font-medium tracking-wide text-neutral-500 uppercase">
        {{ section.label }}
      </p>
      <h1 class="text-2xl font-semibold tracking-tight">{{ route.meta.title }}</h1>
    </div>
    <p v-if="section.intro" class="max-w-3xl text-sm text-neutral-500">{{ section.intro }}</p>
    <p v-if="config.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ config.error }}
    </p>
    <p v-if="blocked" class="text-sm text-neutral-500">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>
    <RouterView v-else-if="!route.meta.needsConfig || config.draft" />
  </div>
</template>
