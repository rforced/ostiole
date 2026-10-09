<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { SCHEDULE_PRESETS, presetFor } from '@/lib/schedules'

/**
 * How often a router asks what is waiting, how often it installs it, and
 * how much of what it finds it may install. Both update cards ask the
 * same three questions, so they ask them the same way.
 */
const props = defineProps({
  /** Prefix for the field ids, so two of these can share a page. */
  prefix: { type: String, required: true },
  /** Empty means defaultMode. */
  mode: { type: String, default: '' },
  /** What an unset mode means on this router: manual where the distro has no security channel. */
  defaultMode: { type: String, default: 'security' },
  checkSchedule: { type: String, default: '' },
  installSchedule: { type: String, default: '' },
  /** False where the distro has no security-only channel. */
  securityCapable: { type: Boolean, default: true },
  /** Why security is unavailable, shown beside the disabled choice. */
  securityNote: { type: String, default: '' },
  /** What is left on disabled, shown in place of the schedules. */
  disabledNote: { type: String, default: 'Nothing happens on its own. Check now still works.' },
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
  {
    value: 'disabled',
    label: 'Disabled',
    hint: 'Never checks or installs on its own.',
  },
]

/** An unset mode is the default rather than a fifth choice. */
const current = computed(() => props.mode || props.defaultMode)

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
    <fieldset class="field-group">
      <legend>Mode</legend>
      <ToggleRow
        v-for="m in MODES"
        :id="`${prefix}-mode-${m.value}`"
        :key="m.value"
        :model-value="current"
        variant="radio"
        :name="`${prefix}-mode`"
        :value="m.value"
        :label="m.label"
        :hint="m.value === 'security' && !securityCapable ? securityNote : m.hint"
        :disabled="m.value === 'security' && !securityCapable"
        @update:model-value="emit('update:mode', $event)"
      />
    </fieldset>

    <!-- The check runs whatever the mode is, unless the mode is
         disabled: a router that installs nothing still has to be able to
         say what is waiting. -->
    <div v-if="current !== 'disabled'" class="form-row">
      <FormField :id="`${prefix}-check-preset`" label="Check for updates">
        <select
          :id="`${prefix}-check-preset`"
          v-model="checkPreset"
          class="input w-48 max-sm:w-full"
        >
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
          class="input w-48 font-mono max-sm:w-full"
          :value="checkSchedule || defaultCheckSchedule"
          :placeholder="defaultCheckSchedule"
          @change="emit('update:checkSchedule', $event.target.value)"
        />
      </FormField>
    </div>

    <div v-if="current !== 'manual' && current !== 'disabled'" class="form-row">
      <FormField :id="`${prefix}-install-preset`" label="Install automatically">
        <select
          :id="`${prefix}-install-preset`"
          v-model="installPreset"
          class="input w-48 max-sm:w-full"
        >
          <option value="">Something else</option>
          <option v-for="p in SCHEDULE_PRESETS" :key="p.value" :value="p.value">
            {{ p.label }}
          </option>
        </select>
      </FormField>
      <FormField :id="`${prefix}-install-schedule`" label="Install schedule">
        <input
          :id="`${prefix}-install-schedule`"
          class="input w-48 font-mono max-sm:w-full"
          :value="installSchedule || defaultInstallSchedule"
          :placeholder="defaultInstallSchedule"
          @change="emit('update:installSchedule', $event.target.value)"
        />
      </FormField>
    </div>
    <p v-else class="text-sm text-ink-muted">
      {{ current === 'disabled' ? disabledNote : 'Nothing is installed on a schedule.' }}
    </p>
  </div>
</template>
