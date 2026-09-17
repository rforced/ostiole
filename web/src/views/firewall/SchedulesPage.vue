<script setup>
import { Plus } from 'lucide-vue-next'
import { ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { useConfigStore } from '@/stores/config'
import ScheduleDialog from '@/views/firewall/ScheduleDialog.vue'

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
  <div class="space-y-3">
    <p class="text-sm text-neutral-500">Times are this firewall's local time.</p>
    <button type="button" class="btn-secondary" @click="add">
      <Plus class="mr-1 size-4" aria-hidden="true" /> Add schedule
    </button>
    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Days</th>
            <th>Window</th>
            <th>Description</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!config.schedules.length" key="empty" class="row-static">
            <td colspan="5" class="text-neutral-500">No schedules.</td>
          </tr>
          <tr
            v-for="s in config.schedules"
            :key="s.name"
            :class="{ 'row-changed': config.isChanged('schedules', s.name) }"
          >
            <td class="font-mono font-medium">{{ s.name }}</td>
            <td>{{ days(s) }}</td>
            <td class="font-mono text-code">
              {{ s.start }} – {{ s.end }}
              <span v-if="s.end < s.start" class="badge ml-1">over midnight</span>
            </td>
            <td>{{ s.description }}</td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="edit(s)">Edit</button>
              <ConfirmButton
                v-if="config.scheduleReferences(s.name).length === 0"
                class="ml-3"
                label="Delete"
                :question="`Delete schedule ${s.name}?`"
                :description="s.description"
                @confirm="config.removeSchedule(s.name)"
              />
              <span v-else class="ml-3 text-sm text-neutral-500"
                >in use by {{ config.scheduleReferences(s.name).join(', ') }}</span
              >
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </div>

    <ScheduleDialog v-model:open="open" :schedule="editing" />
  </div>
</template>
