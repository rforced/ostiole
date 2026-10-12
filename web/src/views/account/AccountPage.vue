<script setup>
import PageHeader from '@/components/PageHeader.vue'
import MfaCard from '@/views/account/MfaCard.vue'
import PasswordCard from '@/views/account/PasswordCard.vue'
import SessionsCard from '@/views/account/SessionsCard.vue'
import TokensCard from '@/views/account/TokensCard.vue'
</script>

<template>
  <div class="space-y-5">
    <PageHeader title="Account" />
    <PasswordCard />
    <MfaCard />
    <TokensCard />
    <SessionsCard />
  </div>
</template>
