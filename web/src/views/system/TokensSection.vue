<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { type Token, api } from '@/lib/api'
import { emptyText, errorMessage, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'

const tokens = ref<Token[]>([])
const actionError = ref('')

const load = useAsync(async () => {
  tokens.value = await api.tokens.list()
})
onMounted(load.run)

const error = computed(() => actionError.value || load.error.value)

async function deleteToken(id: string) {
  actionError.value = ''
  try {
    await api.tokens.remove(id)
    await load.run()
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}
</script>

<template>
  <SectionCard title="API tokens" :count="tokens.length" flush>
    <template #intro>Every account's tokens. New ones are made on the Account page.</template>
    <template #actions>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    </template>

    <div v-if="error" class="card-strip">
      <ErrorLine>{{ error }}</ErrorLine>
    </div>

    <table class="table table-stack">
      <thead>
        <tr>
          <th>Token</th>
          <th>Owner</th>
          <th>Role</th>
          <th>Created</th>
          <th>Expires</th>
          <th>Last used</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!tokens.length">
          <td colspan="7" class="text-ink-muted">
            {{ emptyText(load, 'No tokens.') }}
          </td>
        </tr>
        <tr v-for="t in tokens" :key="t.id">
          <td data-label="">
            <div class="font-medium">{{ t.name }}</div>
            <div class="font-mono text-code text-ink-muted">{{ t.id }}</div>
          </td>
          <td class="font-mono text-code" data-label="Owner">
            <template v-if="t.owner">{{ t.owner }}</template>
            <span v-else class="text-ink-muted">router</span>
          </td>
          <td class="font-mono text-code" data-label="Role">
            <template v-if="(t.certificates ?? []).length">
              certificates: {{ (t.certificates ?? []).join(', ') }}
            </template>
            <template v-else-if="t.metrics">metrics only</template>
            <template v-else>{{ t.role }}</template>
          </td>
          <td class="when" data-label="Created">{{ formatWhen(t.createdAt) }}</td>
          <td class="when" data-label="Expires">{{ formatWhen(t.expiresAt, 'never') }}</td>
          <td class="when" data-label="Last used">{{ formatWhen(t.lastUsedAt, 'never') }}</td>
          <td class="actions" data-label="">
            <ConfirmButton
              label="Delete"
              :question="`Delete token ${t.name}?`"
              description="Anything using it stops working."
              :typed="t.name"
              @confirm="deleteToken(t.id)"
            />
          </td>
        </tr>
      </tbody>
    </table>
  </SectionCard>
</template>
