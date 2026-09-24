<script setup>
import { Plus } from 'lucide-vue-next'
import { ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { someOf } from '@/lib/lists'
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
      intro="Times are this firewall's local time."
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
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!config.schedules.length">
            <td colspan="5" class="text-ink-muted">No schedules.</td>
          </tr>
          <tr
            v-for="s in config.schedules"
            :key="s.name"
            :class="{ 'row-changed': config.isChanged('schedules', s.name) }"
          >
            <td class="font-mono font-medium" data-label="">{{ s.name }}</td>
            <td data-label="Days">{{ days(s) }}</td>
            <td class="font-mono text-code" data-label="Window">
              {{ s.start }} – {{ s.end }}
              <span v-if="s.end < s.start" class="badge ml-1">over midnight</span>
            </td>
            <td data-label="Description">{{ s.description }}</td>
            <td class="text-right whitespace-nowrap" data-label="">
              <button type="button" class="link" @click="edit(s)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                v-if="config.scheduleReferences(s.name).length === 0"
                class="ml-3"
                label="Delete"
                :question="`Delete schedule ${s.name}?`"
                :description="s.description"
                @confirm="config.removeSchedule(s.name)"
              />
              <span
                v-else
                class="ml-3 text-sm text-ink-muted"
                :title="config.scheduleReferences(s.name).join(', ')"
              >
                In use by {{ someOf(config.scheduleReferences(s.name)) }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <ScheduleDialog v-model:open="open" :schedule="editing" />
  </div>
</template>
