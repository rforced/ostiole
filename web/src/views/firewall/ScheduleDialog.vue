<script setup>
import { ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const DAYS = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday']

const props = defineProps({ schedule: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return { name: '', description: '', days: [], start: '08:00', end: '17:00' }
}

watch(
  () => [open.value, props.schedule],
  () => {
    if (open.value)
      form.value = props.schedule
        ? { ...blank(), ...props.schedule, days: [...(props.schedule.days ?? [])] }
        : blank()
  },
  { immediate: true },
)

function toggleDay(day, on) {
  const set = new Set(form.value.days)
  if (on) set.add(day)
  else set.delete(day)
  form.value.days = DAYS.filter((d) => set.has(d))
}

function save() {
  const f = form.value
  const out = { name: f.name.trim(), start: f.start, end: f.end }
  if (f.description) out.description = f.description
  if (f.days.length && f.days.length < DAYS.length) out.days = f.days
  config.upsertSchedule(out, props.schedule?.name ?? out.name)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="schedule ? `Schedule ${schedule.name}` : 'New schedule'"
    description="Rules that name this schedule match only inside the window. An end before the start runs over midnight."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="sch-name" label="Name" hint="Lower case, e.g. workday.">
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
        <FormField id="sch-start" label="From">
          <input id="sch-start" v-model="form.start" type="time" class="input w-32 font-mono" />
        </FormField>
        <FormField id="sch-end" label="To">
          <input id="sch-end" v-model="form.end" type="time" class="input w-32 font-mono" />
        </FormField>
      </div>
      <fieldset class="space-y-2 text-sm">
        <legend class="font-medium">Days</legend>
        <p class="text-xs text-neutral-500">None selected means every day.</p>
        <div class="flex flex-wrap gap-4">
          <label v-for="d in DAYS" :key="d" class="flex items-center gap-2 capitalize">
            <input
              type="checkbox"
              class="size-4 rounded border-neutral-300"
              :checked="form.days.includes(d)"
              @change="toggleDay(d, $event.target.checked)"
            />
            {{ d }}
          </label>
        </div>
      </fieldset>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.name">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
