<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { newId } from '@/lib/ids'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ cron: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const error = ref('')
const form = ref(blank())

const JOBS = [
  { value: 'backup', label: 'Back up the configuration' },
  { value: 'refresh-aliases', label: 'Fetch the blocklists and country ranges' },
  { value: 'restart-service', label: 'Restart a service' },
  { value: 'command', label: 'Run a command' },
]

// The schedules people actually want, so nobody has to remember the
// field order to get a nightly backup.
const PRESETS = [
  { value: '0 4 * * *', label: 'Every night at 04:00' },
  { value: '0 * * * *', label: 'Every hour' },
  { value: '*/15 * * * *', label: 'Every 15 minutes' },
  { value: '0 4 * * 0', label: 'Sunday at 04:00' },
  { value: '0 4 1 * *', label: 'The first of the month at 04:00' },
]

function blank() {
  return {
    id: '',
    description: '',
    enabled: true,
    schedule: '0 4 * * *',
    job: 'backup',
    directory: '/var/backups/ostiole',
    keep: 10,
    withUsers: false,
    service: 'dnsmasq',
    command: '',
    args: '',
    timeoutSeconds: 300,
  }
}

const preset = computed({
  get: () => (PRESETS.some((p) => p.value === form.value.schedule) ? form.value.schedule : ''),
  set: (v) => {
    if (v) form.value.schedule = v
  },
})

watch(
  () => [open.value, props.cron],
  () => {
    if (!open.value) return
    error.value = ''
    const c = props.cron
    form.value = c ? { ...blank(), ...c, args: (c.args ?? []).join('\n') } : blank()
  },
  { immediate: true },
)

function save() {
  error.value = ''
  const f = form.value
  const out = {
    id: f.id || newId('cron'),
    enabled: f.enabled,
    schedule: f.schedule.trim(),
    job: f.job,
  }
  if (f.description) out.description = f.description
  switch (f.job) {
    case 'backup':
      if (!f.directory.trim().startsWith('/')) {
        error.value = 'The backup directory must be an absolute path.'
        return
      }
      out.directory = f.directory.trim()
      if (Number(f.keep) > 0) out.keep = Number(f.keep)
      if (f.withUsers) out.withUsers = true
      break
    case 'restart-service':
      out.service = f.service
      break
    case 'command':
      if (!f.command.trim().startsWith('/')) {
        error.value = 'Give the full path to the command, so it does not depend on a PATH.'
        return
      }
      out.command = f.command.trim()
      if (f.args.trim()) out.args = parseList(f.args)
      if (Number(f.timeoutSeconds) > 0) out.timeoutSeconds = Number(f.timeoutSeconds)
      break
  }
  config.upsertCron(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="cron ? `Job ${cron.description || cron.id}` : 'New scheduled job'"
    description="Runs on this box, as root, on the schedule you give. It is saved with the rest of the configuration, so it is backed up and rolled back with everything else."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="cron-desc" label="Description">
          <input
            id="cron-desc"
            v-model="form.description"
            class="input"
            placeholder="Nightly backup"
          />
        </FormField>
        <FormField id="cron-job" label="What it does">
          <select id="cron-job" v-model="form.job" class="input">
            <option v-for="j in JOBS" :key="j.value" :value="j.value">{{ j.label }}</option>
          </select>
        </FormField>
        <FormField id="cron-preset" label="When">
          <select id="cron-preset" v-model="preset" class="input">
            <option value="">Something else</option>
            <option v-for="p in PRESETS" :key="p.value" :value="p.value">{{ p.label }}</option>
          </select>
        </FormField>
        <FormField
          id="cron-schedule"
          label="Schedule"
          hint="Five fields: minute hour day month weekday. @daily and friends work too."
        >
          <input
            id="cron-schedule"
            v-model="form.schedule"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
      </div>

      <template v-if="form.job === 'backup'">
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField id="cron-dir" label="Write to" hint="An absolute path on this box.">
            <input
              id="cron-dir"
              v-model="form.directory"
              class="input font-mono"
              spellcheck="false"
            />
          </FormField>
          <FormField id="cron-keep" label="Keep" hint="Older backups beyond this are removed.">
            <input
              id="cron-keep"
              v-model.number="form.keep"
              type="number"
              min="1"
              max="1000"
              class="input w-24 font-mono"
            />
          </FormField>
        </div>
        <label class="flex items-center gap-2 text-sm">
          <input
            v-model="form.withUsers"
            type="checkbox"
            class="size-4 rounded border-neutral-300"
          />
          Include the administrator accounts and their password hashes
        </label>
      </template>

      <FormField v-if="form.job === 'restart-service'" id="cron-service" label="Service">
        <select id="cron-service" v-model="form.service" class="input">
          <option value="dnsmasq">dnsmasq (DHCP and DNS)</option>
          <option value="unbound">unbound (the resolver)</option>
          <option value="ostiole">ostiole (this daemon)</option>
        </select>
      </FormField>

      <template v-if="form.job === 'command'">
        <FormField
          id="cron-command"
          label="Command"
          hint="The full path. It is run directly, not through a shell, so there is nothing to quote."
        >
          <input
            id="cron-command"
            v-model="form.command"
            class="input font-mono"
            placeholder="/usr/bin/systemctl"
            spellcheck="false"
          />
        </FormField>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField id="cron-args" label="Arguments" hint="One per line.">
            <textarea
              id="cron-args"
              v-model="form.args"
              class="input h-24 font-mono text-xs"
              spellcheck="false"
            ></textarea>
          </FormField>
          <FormField
            id="cron-timeout"
            label="Give up after (seconds)"
            hint="A job that hangs is a job that never runs again."
          >
            <input
              id="cron-timeout"
              v-model.number="form.timeoutSeconds"
              type="number"
              min="1"
              max="3600"
              class="input w-28 font-mono"
            />
          </FormField>
        </div>
      </template>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
        Enabled
      </label>

      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
