<script setup>
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'

import ErrorLine from '@/components/ErrorLine.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()
const route = useRoute()

/** A page that edits the draft has nothing to show until the wizard has made one. */
const blocked = computed(() => route.meta.needsConfig && config.loaded && !config.draft)

onMounted(() => config.load())
</script>

<template>
  <div class="space-y-5">
    <PageHeader v-if="!route.meta.ownHeader" />
    <ErrorLine v-if="config.error" class="text-sm">
      {{ config.error }}
    </ErrorLine>
    <p v-if="blocked" class="text-sm text-ink-muted">
      No configuration yet.
      <template v-if="!auth.readOnly">
        <RouterLink to="/wizard" class="link">Run the setup wizard</RouterLink> first.
      </template>
    </p>
    <RouterView v-else-if="!route.meta.needsConfig || config.draft" />
  </div>
</template>
