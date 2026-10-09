<script setup>
import { Plus } from 'lucide-vue-next'
import { ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import UsedByCell from '@/components/UsedByCell.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ScheduleDialog from '@/views/firewall/ScheduleDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const editing = ref(null)
const open = ref(false)

function add() {
  editing.value = null
  open.value = true
}
function edit(s) {
  editing.value = s
  open.value = true
}

/** "Mon–Fri 08:30–17:30" style summary. */
function days(schedule) {
  if (!schedule.days?.length) return 'every day'
  return schedule.days.map((d) => d.slice(0, 3)).join(', ')
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Schedules"
      :count="config.schedules.length"
      intro="Times are this router's local time."
      flush
    >
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add schedule
        </button>
      </template>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Name</th>
            <th>Days</th>
            <th>Window</th>
            <th>Description</th>
            <th>Used by</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!config.schedules.length">
            <td colspan="6" class="text-ink-muted">No schedules.</td>
          </tr>
          <tr
            v-for="s in config.schedules"
            :key="s.name"
            :class="{ 'row-changed': config.isChanged('schedules', s.name) }"
          >
            <td class="font-mono font-medium" data-label="">{{ s.name }}</td>
            <td data-label="Days">{{ days(s) }}</td>
            <td class="font-mono text-code" data-label="Window">
              <span class="inline-flex items-center gap-1.5">
                {{ s.start }} – {{ s.end }}
                <span v-if="s.end < s.start" class="badge">over midnight</span>
              </span>
            </td>
            <td data-label="Description">{{ s.description }}</td>
            <UsedByCell :names="config.scheduleReferences(s.name)" />
            <td class="actions" data-label="">
              <button type="button" class="link-action" @click="edit(s)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                label="Delete"
                :question="`Delete schedule ${s.name}?`"
                :description="s.description"
                :disabled="config.scheduleReferences(s.name).length > 0"
                @confirm="config.removeSchedule(s.name)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <ScheduleDialog v-model:open="open" :schedule="editing" />
  </div>
</template>
