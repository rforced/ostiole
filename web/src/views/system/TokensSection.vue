<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { ApiError, api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import TokenDialog from '@/views/system/accounts/TokenDialog.vue'

const tokens = ref([])
const actionError = ref('')
/** Only an admin may list tokens; for anybody else the card is not there. */
const available = ref(true)
const adding = ref(false)
/** The one moment a new token's secret exists outside the server. */
const minted = ref(null)

const load = useAsync(async () => {
  try {
    tokens.value = await api.tokens.list()
    available.value = true
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) {
      available.value = false
      return
    }
    throw e
  }
})
onMounted(load.run)

const error = computed(() => actionError.value || load.error.value)

async function createToken(token) {
  actionError.value = ''
  try {
    minted.value = await api.tokens.create(token)
    adding.value = false
    await load.run()
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}

async function deleteToken(id) {
  actionError.value = ''
  try {
    await api.tokens.remove(id)
    await load.run()
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}

const when = (s, fallback) => (s ? new Date(s).toLocaleDateString() : fallback)
</script>

<template>
  <SectionCard v-if="available" title="API tokens" :count="tokens.length" flush>
    <template #intro>
      Sent as <span class="font-mono">Authorization: Bearer ost_…</span>.
    </template>
    <template #actions>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
      <button type="button" class="btn-secondary" @click="adding = true">
        <Plus class="size-4" aria-hidden="true" /> Add token
      </button>
    </template>

    <div v-if="minted || error" class="space-y-3 px-4 pb-3">
      <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
      <AppNotice
        v-if="minted"
        :title="`Copy ${minted.name} now. It is not stored and cannot be shown again.`"
      >
        <code class="block font-mono text-code break-all select-all">{{ minted.secret }}</code>
        <button type="button" class="btn-secondary mt-2" @click="minted = null">Close</button>
      </AppNotice>
    </div>

    <table class="table">
      <thead>
        <tr>
          <th>Token</th>
          <th>Role</th>
          <th>Created</th>
          <th>Expires</th>
          <th>Last used</th>
          <th></th>
        </tr>
      </thead>
      <TransitionGroup name="row" tag="tbody">
        <tr v-if="!tokens.length" key="empty" class="row-static">
          <td colspan="6" class="text-ink-muted">
            {{ load.updatedAt.value ? 'No tokens.' : 'Reading…' }}
          </td>
        </tr>
        <tr v-for="t in tokens" :key="t.id">
          <td>
            <div class="font-medium">{{ t.name }}</div>
            <div class="font-mono text-code text-ink-muted">{{ t.id }}</div>
          </td>
          <td class="font-mono text-code">
            <template v-if="(t.certificates ?? []).length">
              certificates: {{ t.certificates.join(', ') }}
            </template>
            <template v-else>{{ t.role }}</template>
          </td>
          <td>{{ when(t.createdAt, '—') }}</td>
          <td>{{ when(t.expiresAt, 'never') }}</td>
          <td>{{ when(t.lastUsedAt, 'never') }}</td>
          <td class="text-right">
            <ConfirmButton
              label="Delete"
              :question="`Delete token ${t.name}?`"
              description="Anything using it stops working."
              :typed="t.name"
              @confirm="deleteToken(t.id)"
            />
          </td>
        </tr>
      </TransitionGroup>
    </table>

    <TokenDialog v-model:open="adding" @create="createToken" />
  </SectionCard>
</template>
