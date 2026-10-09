<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { newId } from '@/lib/ids'
import { SCHEDULE_PRESETS, presetFor } from '@/lib/schedules'
import { deviceName } from '@/lib/wol'
import { ADMIN_ONLY, adminOnly, useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { BACKUP_DIR } from '@/views/system/crons/backup'
import { SERVICES } from '@/views/system/crons/services'

const props = defineProps({ cron: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const auth = useAuthStore()
const config = useConfigStore()
const error = ref('')
const form = ref(blank())

/** The cron kinds the model offers, in the order model.CronKinds lists them. */
const KINDS = [
  { value: 'backup', label: 'Back up the configuration' },
  { value: 'refresh-aliases', label: 'Fetch the address lists and country ranges' },
  { value: 'refresh-blocklists', label: 'Fetch the DNS blocklists' },
  { value: 'restart-service', label: 'Restart a service' },
  { value: 'wake', label: 'Wake a device' },
  { value: 'command', label: 'Run a command' },
]

function blank() {
  return {
    id: '',
    description: '',
    enabled: true,
    schedule: '0 4 * * *',
    kind: 'backup',
    keep: '',
    withUsers: false,
    passphrase: '',
    service: 'dnsmasq',
    device: config.wolDevices[0]?.id ?? '',
    command: '',
    args: '',
    timeoutSeconds: '',
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
      if (Number(f.keep) > 0) out.keep = Number(f.keep)
      if (f.withUsers) out.withUsers = true
      if (f.passphrase) out.passphrase = f.passphrase
      else delete out.passphrase
      break
    case 'restart-service':
      out.service = f.service
      break
    case 'wake':
      if (!f.device) {
        error.value = 'Add a device on the Wake on LAN page first.'
        return
      }
      out.device = f.device
      break
    case 'command':
      if (!f.command.trim().startsWith('/')) {
        error.value = 'Give the full path to the command, so it does not depend on a PATH.'
        return
      }
      out.command = f.command.trim()
      // A line is one argument; no shell splits it on spaces or commas.
      out.args = f.args
        .split('\n')
        .map((a) => a.trim())
        .filter(Boolean)
      if (!out.args.length) delete out.args
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
    :title="cron ? `Cron job ${cron.description || cron.id}` : 'Add cron job'"
    description="Runs on this router, as root, on the schedule you give."
  >
    <form id="cron-form" class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField id="cron-desc" label="Description">
          <input id="cron-desc" v-model="form.description" class="input" placeholder="optional" />
        </FormField>
        <FormField
          id="cron-kind"
          label="What it does"
          :hint="auth.isOperator ? adminOnly('add', 'one that runs a command') : ''"
        >
          <select id="cron-kind" v-model="form.kind" class="input">
            <option
              v-for="k in KINDS"
              :key="k.value"
              :value="k.value"
              :disabled="k.value === 'command' && !auth.isAdmin"
            >
              {{ k.label }}
            </option>
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
        <dl class="kv text-sm">
          <dt>Written to</dt>
          <dd class="font-mono text-code">{{ BACKUP_DIR }}</dd>
        </dl>
        <div class="fields">
          <FormField
            id="cron-keep"
            label="Keep"
            hint="10 is the default. Older backups beyond it are removed."
          >
            <input
              id="cron-keep"
              v-model.number="form.keep"
              type="number"
              min="1"
              max="1000"
              placeholder="10"
              class="input w-32 font-mono max-sm:w-full"
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
        <div class="text-sm">
          <ToggleRow
            v-model="form.withUsers"
            label="Include the administrator accounts and their password hashes"
            :hint="auth.isOperator ? ADMIN_ONLY : ''"
            :disabled="!auth.isAdmin"
          />
        </div>
      </template>

      <FormField v-if="form.kind === 'restart-service'" id="cron-service" label="Service">
        <select id="cron-service" v-model="form.service" class="input">
          <option v-for="s in SERVICES" :key="s.value" :value="s.value">{{ s.label }}</option>
        </select>
      </FormField>

      <FormField v-if="form.kind === 'wake'" id="cron-device" label="Device">
        <select id="cron-device" v-model="form.device" class="input">
          <option v-if="!config.wolDevices.length" value="">None on the Wake on LAN page</option>
          <option v-for="d in config.wolDevices" :key="d.id" :value="d.id">
            {{ deviceName(d) }}
          </option>
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
            spellcheck="false"
          />
        </FormField>
        <div class="fields">
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
            hint="300 is the default. A command that hangs is one that never runs again."
          >
            <input
              id="cron-timeout"
              v-model.number="form.timeoutSeconds"
              type="number"
              min="1"
              max="3600"
              placeholder="300"
              class="input w-32 font-mono max-sm:w-full"
            />
          </FormField>
        </div>
      </template>

      <ToggleRow v-model="form.enabled" label="Enabled" />

      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="cron-form" class="btn-primary">Save to draft</button>
    </template>
  </AppDialog>
</template>
