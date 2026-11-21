<script setup lang="ts">
import { ref, watch } from 'vue'
import type { PropType } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import { nameProblem } from '@/lib/names'
import type { Schedule } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'

const DAYS = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday']

const props = defineProps({
  schedule: { type: Object as PropType<Schedule | null>, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

interface ScheduleForm {
  name: string
  description: string
  days: string[]
  start: string
  end: string
}

const config = useConfigStore()
const form = ref<ScheduleForm>(blank())
const error = ref('')

function blank(): ScheduleForm {
  return { name: '', description: '', days: [], start: '08:00', end: '17:00' }
}

watch(
  () => [open.value, props.schedule],
  () => {
    error.value = ''
    if (open.value)
      form.value = props.schedule
        ? { ...blank(), ...props.schedule, days: [...(props.schedule.days ?? [])] }
        : blank()
  },
  { immediate: true },
)

function toggleDay(day: string, on: boolean) {
  const set = new Set(form.value.days)
  if (on) set.add(day)
  else set.delete(day)
  form.value.days = DAYS.filter((d) => set.has(d))
}

function save() {
  const f = form.value
  const name = f.name.trim()
  const previous = props.schedule?.name ?? name
  error.value = nameProblem('name', name)
  if (
    !error.value &&
    name !== props.schedule?.name &&
    config.schedules.some((s) => s.name === name)
  )
    error.value = `Schedule ${name} already exists.`
  if (error.value) return
  const out: Schedule = { name, start: f.start, end: f.end }
  if (f.description) out.description = f.description
  const every = f.days.length === DAYS.length && props.schedule?.days?.length !== DAYS.length
  if (f.days.length && !every) out.days = f.days
  config.upsertSchedule(out, previous)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="schedule ? `Schedule ${schedule.name}` : 'Add schedule'"
    description="Rules that name this schedule match only inside the window. An end before the start runs over midnight."
  >
    <form id="schedule-form" class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField id="sch-name" label="Name" hint="Lower case, like workday.">
          <input
            id="sch-name"
            v-model="form.name"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="sch-desc" label="Description">
          <input id="sch-desc" v-model="form.description" class="input" />
        </FormField>
        <!-- A time field is as wide as the browser draws it: the segments and
             the clock button, and an AM/PM segment where the locale has one.
             A width of ours clips whichever end does not fit. -->
        <FormField id="sch-start" label="From">
          <input id="sch-start" v-model="form.start" type="time" class="input w-auto font-mono" />
        </FormField>
        <FormField id="sch-end" label="To">
          <input id="sch-end" v-model="form.end" type="time" class="input w-auto font-mono" />
        </FormField>
      </div>
      <fieldset class="field-group text-sm">
        <legend>Days</legend>
        <p class="text-sm text-ink-muted">None selected means every day.</p>
        <div class="flex flex-wrap gap-4">
          <label v-for="d in DAYS" :key="d" class="flex items-center gap-2 capitalize">
            <input
              type="checkbox"
              class="checkbox"
              :checked="form.days.includes(d)"
              @change="toggleDay(d, ($event.target as HTMLInputElement).checked)"
            />
            {{ d }}
          </label>
        </div>
      </fieldset>
      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="schedule-form" class="btn-primary" :disabled="!form.name">
        Save to draft
      </button>
    </template>
  </AppDialog>
</template>
