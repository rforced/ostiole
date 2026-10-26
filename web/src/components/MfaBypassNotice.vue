<script setup lang="ts">
import { computed } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import { formatWhen } from '@/lib/format'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()

const text = computed(() => {
  const until = auth.user?.bypassUntil
  if (!until) return ''
  const by = auth.user.bypassBy ? ` by ${auth.user.bypassBy}` : ''
  return `MFA is bypassed until ${formatWhen(until)}${by}, from the command line.`
})
</script>

<template>
  <div
    v-if="text"
    class="px-6 pt-6 max-lg:px-[max(1.5rem,env(safe-area-inset-left),env(safe-area-inset-right))] max-sm:px-4 max-sm:pt-4"
  >
    <AppNotice kind="warn">{{ text }}</AppNotice>
  </div>
</template>
