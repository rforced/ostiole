<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import { SCHEDULE_PRESETS, presetFor } from '@/lib/schedules'

/**
 * How often a router asks what is waiting, how often it installs it, and
 * how much of what it finds it may install. Both update cards ask the
 * same three questions, so they ask them the same way.
 */
const props = defineProps({
  /** Prefix for the field ids, so two of these can share a page. */
  prefix: { type: String, required: true },
  /** Empty means the default, which the daemon fills in as security. */
  mode: { type: String, default: '' },
  checkSchedule: { type: String, default: '' },
  installSchedule: { type: String, default: '' },
  /** False where the distro has no security-only channel. */
  securityCapable: { type: Boolean, default: true },
  /** Why security is unavailable, shown beside the disabled choice. */
  securityNote: { type: String, default: '' },
  /** The schedules used when none is set, shown as the placeholders. */
  defaultCheckSchedule: { type: String, default: '0 4 * * *' },
  defaultInstallSchedule: { type: String, default: '30 4 * * 0' },
})
const emit = defineEmits(['update:mode', 'update:checkSchedule', 'update:installSchedule'])

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
    hint: 'Check on the schedule and install nothing.',
  },
]

/** An unset mode is the default rather than a fourth choice. */
const current = computed(() => props.mode || 'security')

const checkPreset = computed({
  get: () => presetFor(props.checkSchedule || props.defaultCheckSchedule),
  set: (v) => {
    if (v) emit('update:checkSchedule', v)
  },
})

const installPreset = computed({
  get: () => presetFor(props.installSchedule || props.defaultInstallSchedule),
  set: (v) => {
    if (v) emit('update:installSchedule', v)
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

    <!-- The check runs whatever the mode is: a router that installs
         nothing still has to be able to say what is waiting. -->
    <div class="form-row">
      <FormField :id="`${prefix}-check-preset`" label="Check for updates">
        <select :id="`${prefix}-check-preset`" v-model="checkPreset" class="input w-64">
          <option value="">Something else</option>
          <option v-for="p in SCHEDULE_PRESETS" :key="p.value" :value="p.value">
            {{ p.label }}
          </option>
        </select>
      </FormField>
      <FormField
        :id="`${prefix}-check-schedule`"
        label="Check schedule"
        hint="Five fields: minute hour day month weekday."
      >
        <input
          :id="`${prefix}-check-schedule`"
          class="input w-48 font-mono"
          :value="checkSchedule || defaultCheckSchedule"
          :placeholder="defaultCheckSchedule"
          @change="emit('update:checkSchedule', $event.target.value)"
        />
      </FormField>
    </div>

    <div v-if="current !== 'manual'" class="form-row">
      <FormField :id="`${prefix}-install-preset`" label="Install automatically">
        <select :id="`${prefix}-install-preset`" v-model="installPreset" class="input w-64">
          <option value="">Something else</option>
          <option v-for="p in SCHEDULE_PRESETS" :key="p.value" :value="p.value">
            {{ p.label }}
          </option>
        </select>
      </FormField>
      <FormField :id="`${prefix}-install-schedule`" label="Install schedule">
        <input
          :id="`${prefix}-install-schedule`"
          class="input w-48 font-mono"
          :value="installSchedule || defaultInstallSchedule"
          :placeholder="defaultInstallSchedule"
          @change="emit('update:installSchedule', $event.target.value)"
        />
      </FormField>
    </div>
    <p v-else class="text-sm text-neutral-500">Nothing is installed on a schedule.</p>
  </div>
</template>
