<script setup>
import { computed, ref } from 'vue'

import ClearLogButton from '@/components/ClearLogButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import LiveButton from '@/components/LiveButton.vue'
import LoadMore from '@/components/LoadMore.vue'
import SearchBox from '@/components/SearchBox.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { actorParts, auditValues, who } from '@/lib/audit'
import { formatWhen } from '@/lib/format'
import { heldLine, useLog } from '@/lib/log'

/** Who changed what on the router, newest first. */
const card = ref(null)
const log = useLog({
  read: (params, signal) => api.audit.log(params, signal),
  stream: '/api/v1/audit/stream',
  values: auditValues,
  top: card,
})
const { rows, query, live } = log

/** Empties the log, bar the entry saying who did, and reads it again. */
const clear = useAsync(async () => {
  await api.audit.clear()
  await log.reload()
})

const error = computed(() => log.error.value || clear.error.value || log.streamError.value)

const empty = computed(() => {
  const q = query.value.trim()
  return emptyText(log, q ? `Nothing matches "${q}".` : 'No entries.')
})
</script>

<template>
  <SectionCard ref="card" title="Audit log" flush>
    <template #intro>
      {{ log.updatedAt.value ? heldLine(log.held.value, log.oldest.value) : '' }}
      Kept a year on this router, through restarts and updates.
    </template>
    <template #actions>
      <LiveButton v-model="live" :failing="Boolean(error)" />
      <ClearLogButton
        name="audit log"
        noun="entry"
        description="Every entry is dropped. One stays, saying who cleared it."
        :busy="clear.busy.value"
        @confirm="clear.run()"
      />
    </template>
    <div class="card-strip-row">
      <SearchBox v-model="query" placeholder="name, address, or action" />
      <ErrorLine v-if="error">{{ error }}</ErrorLine>
    </div>
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Time</th>
          <th>Who</th>
          <th>Action</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!rows.length">
          <td colspan="3" class="text-ink-muted">{{ empty }}</td>
        </tr>
        <tr v-for="e in rows" :key="e.seq" v-memo="[e]">
          <td data-label="Time" class="when">
            {{ formatWhen(e.time) }}
          </td>
          <td data-label="Who">
            {{ who(e.by) }}
            <div v-if="actorParts(e.by).length" class="text-xs text-ink-muted">
              {{ actorParts(e.by).join(' · ') }}
            </div>
          </td>
          <td data-label="Action">{{ e.text }}</td>
        </tr>
      </tbody>
    </table>
    <LoadMore
      :more="log.more.value"
      :busy="log.readingMore.value"
      :rows="rows.length"
      :searched-to="log.searchedTo.value"
      @load="log.loadMore()"
    />
  </SectionCard>
</template>
