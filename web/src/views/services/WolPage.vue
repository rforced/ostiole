<script setup>
import { useAuthStore } from '@/stores/auth'
import DevicesCard from '@/views/services/wol/DevicesCard.vue'
import WakeCard from '@/views/services/wol/WakeCard.vue'

const auth = useAuthStore()
</script>

<template>
  <div class="space-y-5">
    <DevicesCard />
    <WakeCard v-if="!auth.readOnly" />
  </div>
</template>
