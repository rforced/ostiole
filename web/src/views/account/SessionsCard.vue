<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { describe } from '@/lib/agent'
import { api } from '@/lib/api'
import { emptyText, errorMessage, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import { useConfirmStore } from '@/stores/confirm'

const confirm = useConfirmStore()
const sessions = ref([])
const actionError = ref('')
/** The id of the session being signed out, '*' for the others, '' for none. */
const ending = ref('')

const load = useAsync(async () => {
  sessions.value = await api.auth.sessions()
})
onMounted(load.run)

const error = computed(() => actionError.value || load.error.value)
const others = computed(() => sessions.value.some((s) => !s.current))

async function end(key, fn) {
  actionError.value = ''
  ending.value = key
  try {
    await fn()
  } catch (e) {
    actionError.value = errorMessage(e)
  } finally {
    ending.value = ''
  }
  await load.run()
}

const endSession = (id) => end(id, () => api.auth.endSession(id))

async function endOthers() {
  const ok = await confirm.ask({
    question: 'Sign out every other session?',
    description: 'They sign in again next time.',
    confirmLabel: 'Sign out',
  })
  if (ok) await end('*', api.auth.endOtherSessions)
}
</script>

<template>
  <SectionCard title="Sessions" :count="sessions.length" flush>
    <template #intro>Browsers signed in as you.</template>
    <template #actions>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
      <ActionButton
        label="Sign out the others"
        busy-label="Signing out…"
        :busy="ending === '*'"
        :disabled="!others || Boolean(ending)"
        @click="endOthers"
      />
    </template>

    <div v-if="error" class="card-strip">
      <ErrorLine>{{ error }}</ErrorLine>
    </div>

    <table class="table table-stack">
      <thead>
        <tr>
          <th>Browser</th>
          <th>Address</th>
          <th>Signed in</th>
          <th>Last seen</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!sessions.length">
          <td colspan="5" class="text-ink-muted">
            {{ emptyText(load, 'No sessions.') }}
          </td>
        </tr>
        <tr v-for="s in sessions" :key="s.id">
          <td data-label="">
            <span class="inline-flex flex-wrap items-center gap-1.5">
              <span :title="s.agent || undefined">{{ describe(s.agent) }}</span>
              <span v-if="s.current" class="text-xs text-ink-muted">this browser</span>
            </span>
          </td>
          <td data-label="Address">
            <span v-if="s.address" class="font-mono text-code">{{ s.address }}</span>
            <span v-else class="text-ink-muted">unknown</span>
          </td>
          <td class="when" data-label="Signed in">{{ formatWhen(s.created) }}</td>
          <td class="when" data-label="Last seen">{{ formatWhen(s.lastSeen) }}</td>
          <td class="actions" data-label="">
            <ActionButton
              v-if="!s.current"
              kind="link"
              label="Sign out"
              busy-label="Signing out…"
              :busy="ending === s.id"
              :disabled="Boolean(ending)"
              @click="endSession(s.id)"
            />
          </td>
        </tr>
      </tbody>
    </table>
  </SectionCard>
</template>
