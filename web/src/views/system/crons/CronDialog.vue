<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { newId } from '@/lib/ids'
import { parseList } from '@/lib/lists'
import { SCHEDULE_PRESETS, presetFor } from '@/lib/schedules'
import { useConfigStore } from '@/stores/config'
import { SERVICES } from '@/views/system/crons/services'

const props = defineProps({ cron: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const error = ref('')
const form = ref(blank())

/** The cron kinds the model offers, in the order model.CronKinds lists them. */
const KINDS = [
  { value: 'backup', label: 'Back up the configuration' },
  { value: 'refresh-aliases', label: 'Fetch the address lists and country ranges' },
  { value: 'refresh-blocklists', label: 'Fetch the DNS blocklists' },
  { value: 'restart-service', label: 'Restart a service' },
  { value: 'command', label: 'Run a command' },
]

function blank() {
  return {
    id: '',
    description: '',
    enabled: true,
    schedule: '0 4 * * *',
    kind: 'backup',
    directory: '/var/backups/ostiole',
    keep: 10,
    withUsers: false,
    passphrase: '',
    service: 'dnsmasq',
    command: '',
    args: '',
    timeoutSeconds: 300,
  }
}

const preset = computed({
  get: () => presetFor(form.value.schedule),
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
    kind: f.kind,
  }
  if (f.description) out.description = f.description
  switch (f.kind) {
    case 'backup':
      if (!f.directory.trim().startsWith('/')) {
        error.value = 'The backup directory must be an absolute path.'
        return
      }
      out.directory = f.directory.trim()
      if (Number(f.keep) > 0) out.keep = Number(f.keep)
      if (f.withUsers) out.withUsers = true
      if (f.passphrase) out.passphrase = f.passphrase
      else delete out.passphrase
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
    :title="cron ? `Cron ${cron.description || cron.id}` : 'Add cron'"
    description="Runs on this router, as root, on the schedule you give."
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
        <FormField id="cron-kind" label="What it does">
          <select id="cron-kind" v-model="form.kind" class="input">
            <option v-for="k in KINDS" :key="k.value" :value="k.value">{{ k.label }}</option>
          </select>
        </FormField>
        <FormField id="cron-preset" label="When">
          <select id="cron-preset" v-model="preset" class="input">
            <option value="">Something else</option>
            <option v-for="p in SCHEDULE_PRESETS" :key="p.value" :value="p.value">
              {{ p.label }}
            </option>
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

      <template v-if="form.kind === 'backup'">
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField id="cron-dir" label="Write to" hint="An absolute path on this router.">
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
          <FormField id="cron-passphrase" label="Passphrase" hint="Empty writes plain JSON.">
            <input
              id="cron-passphrase"
              v-model="form.passphrase"
              type="password"
              class="input"
              autocomplete="new-password"
            />
          </FormField>
        </div>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="form.withUsers" type="checkbox" class="size-4 rounded border-line-2" />
          Include the administrator accounts and their password hashes
        </label>
      </template>

      <FormField v-if="form.kind === 'restart-service'" id="cron-service" label="Service">
        <select id="cron-service" v-model="form.service" class="input">
          <option v-for="s in SERVICES" :key="s.value" :value="s.value">{{ s.label }}</option>
        </select>
      </FormField>

      <template v-if="form.kind === 'command'">
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
              class="input h-24 font-mono"
              spellcheck="false"
            ></textarea>
          </FormField>
          <FormField
            id="cron-timeout"
            label="Give up after (seconds)"
            hint="A command that hangs is one that never runs again."
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
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-line-2" />
        Enabled
      </label>

      <p v-if="error" role="alert" class="text-sm text-bad">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
