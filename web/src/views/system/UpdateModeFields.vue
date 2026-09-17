<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import { SCHEDULE_PRESETS, presetFor } from '@/lib/schedules'

/**
 * How often a box patches itself, and how much of what it finds it
 * installs. Both update cards ask the same two questions, so they ask
 * them the same way.
 */
const props = defineProps({
  /** Prefix for the field ids, so two of these can share a page. */
  prefix: { type: String, required: true },
  /** Empty means the default, which the daemon fills in as security. */
  mode: { type: String, default: '' },
  schedule: { type: String, default: '' },
  /** False where the distro has no security-only channel. */
  securityCapable: { type: Boolean, default: true },
  /** Why security is unavailable, shown beside the disabled choice. */
  securityNote: { type: String, default: '' },
  /** The schedule used when none is set, shown as the placeholder. */
  defaultSchedule: { type: String, default: '0 4 * * 0' },
})
const emit = defineEmits(['update:mode', 'update:schedule'])

const MODES = [
  { value: 'all', label: 'Automatic (All)', hint: 'Install everything that is newer.' },
  {
    value: 'security',
    label: 'Automatic (Security)',
    hint: 'Install only the updates marked as security fixes.',
  },
  {
    value: 'manual',
    label: 'Manual',
    hint: 'Check on the schedule and tell you; install nothing.',
  },
]

/** An unset mode is the default rather than a fourth choice. */
const current = computed(() => props.mode || 'security')

const preset = computed({
  get: () => presetFor(props.schedule || props.defaultSchedule),
  set: (v) => {
    if (v) emit('update:schedule', v)
  },
})
</script>

<template>
  <div class="space-y-3">
    <fieldset class="space-y-1.5">
      <legend class="mb-1 block text-sm font-medium">Mode</legend>
      <!-- The hint describes the choice rather than naming it, so each
           radio is called "Manual" and not "Manual, check on the…". -->
      <div
        v-for="m in MODES"
        :key="m.value"
        class="flex items-start gap-2"
        :class="{ 'opacity-50': m.value === 'security' && !securityCapable }"
      >
        <input
          :id="`${prefix}-mode-${m.value}`"
          type="radio"
          class="mt-1"
          :name="`${prefix}-mode`"
          :value="m.value"
          :checked="current === m.value"
          :disabled="m.value === 'security' && !securityCapable"
          :aria-describedby="`${prefix}-mode-${m.value}-hint`"
          @change="emit('update:mode', m.value)"
        />
        <div>
          <label :for="`${prefix}-mode-${m.value}`" class="block text-sm font-medium">
            {{ m.label }}
          </label>
          <p :id="`${prefix}-mode-${m.value}-hint`" class="text-xs text-neutral-500">
            {{ m.value === 'security' && !securityCapable ? securityNote : m.hint }}
          </p>
        </div>
      </div>
    </fieldset>

    <div class="flex flex-wrap items-end gap-3">
      <FormField :id="`${prefix}-preset`" label="When">
        <select :id="`${prefix}-preset`" v-model="preset" class="input w-64">
          <option value="">Something else</option>
          <option v-for="p in SCHEDULE_PRESETS" :key="p.value" :value="p.value">
            {{ p.label }}
          </option>
        </select>
      </FormField>
      <FormField
        :id="`${prefix}-schedule`"
        label="Schedule"
        hint="Five fields: minute hour day month weekday."
      >
        <input
          :id="`${prefix}-schedule`"
          class="input w-48 font-mono"
          :value="schedule || defaultSchedule"
          :placeholder="defaultSchedule"
          @change="emit('update:schedule', $event.target.value)"
        />
      </FormField>
    </div>
  </div>
</template>
