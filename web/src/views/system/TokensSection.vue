<script setup>
import { Check, Copy, Plus } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

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
const secret = ref(null)
/** 'copied', 'selected' when only the fallback's selection worked, or ''. */
const copy = ref('')
let copyTimer = 0

watch(minted, () => (copy.value = ''))
onBeforeUnmount(() => window.clearTimeout(copyTimer))

/**
 * The clipboard API needs HTTPS or localhost, which a router reached over
 * plain HTTP on its LAN is not, so that falls back to selecting the secret
 * and the older copy command. Either way the secret ends up selected.
 */
async function copySecret() {
  const range = document.createRange()
  range.selectNodeContents(secret.value)
  const selection = window.getSelection()
  selection.removeAllRanges()
  selection.addRange(range)
  let ok
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(minted.value.secret)
      ok = true
    } else ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  copy.value = ok ? 'copied' : 'selected'
  window.clearTimeout(copyTimer)
  copyTimer = window.setTimeout(() => (copy.value = ''), 2000)
}

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

    <div v-if="minted || error" class="card-strip space-y-3">
      <p v-if="error" role="alert" class="text-bad">{{ error }}</p>
      <AppNotice
        v-if="minted"
        :title="`Copy ${minted.name} now. It is not stored and cannot be shown again.`"
      >
        <div class="flex items-start gap-2">
          <code ref="secret" class="min-w-0 flex-1 font-mono text-code break-all select-all">{{
            minted.secret
          }}</code>
          <button type="button" class="btn-secondary shrink-0" @click="copySecret">
            <Check v-if="copy === 'copied'" class="size-4" aria-hidden="true" />
            <Copy v-else class="size-4" aria-hidden="true" />
            {{ copy === 'copied' ? 'Copied' : copy === 'selected' ? 'Selected' : 'Copy' }}
          </button>
        </div>
        <span class="sr-only" aria-live="polite">{{
          copy === 'copied' ? 'Copied.' : copy === 'selected' ? 'Selected, copy it by hand.' : ''
        }}</span>
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
