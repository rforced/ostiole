<script setup>
import { Download, Upload } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ChangeList from '@/components/ChangeList.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'

const config = useConfigStore()
const confirm = useConfirmStore()
const note = ref('')
const withUsers = ref(false)
const pending = ref(null)
const fileInput = ref(null)

const download = useAsync(async () => {
  const { blob, name } = await api.config.backup({ users: withUsers.value, note: note.value })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
})

const restore = useAsync(async (file) => {
  pending.value = null
  pending.value = await api.config.restore(await file.text())
})

const busy = computed(() => download.busy.value || restore.busy.value)
const error = computed(() => download.error.value || restore.error.value)

async function chooseFile(event) {
  const file = event.target.files?.[0]
  if (!file) return
  await restore.run(file)
  if (fileInput.value) fileInput.value.value = ''
}

async function loadIntoDraft() {
  if (
    config.dirty &&
    !(await confirm.ask({
      question: 'Replace the current draft?',
      description: 'Its unapplied changes are lost.',
      confirmLabel: 'Replace',
    }))
  )
    return
  config.replaceDraft(pending.value.config)
  pending.value = null
}

/** "1 rule" or "3 rules"; pass the plural where an -s will not do. */
function count(n, one, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`
}
</script>

<template>
  <section class="card space-y-3" aria-labelledby="backup-title">
    <h2 id="backup-title" class="card-title">Backup and restore</h2>
    <p class="text-sm text-neutral-500">
      The file carries every secret in the configuration, VPN private keys included.
    </p>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <div class="grid gap-4 sm:grid-cols-2">
      <FormField id="bk-note" label="Note" hint="Stored in the file.">
        <input id="bk-note" v-model="note" class="input" placeholder="before the VLAN change" />
      </FormField>
      <div class="flex flex-col justify-end gap-2">
        <label class="flex items-center gap-2 text-sm">
          <input v-model="withUsers" type="checkbox" class="size-4 rounded border-neutral-300" />
          Include administrator accounts
        </label>
        <div class="flex gap-2">
          <button type="button" class="btn-primary" :disabled="busy" @click="download.run">
            <Download class="mr-1 size-4" aria-hidden="true" /> Download backup
          </button>
          <button type="button" class="btn-secondary" :disabled="busy" @click="fileInput?.click()">
            <Upload class="mr-1 size-4" aria-hidden="true" /> Restore from file
          </button>
        </div>
      </div>
    </div>
    <input
      ref="fileInput"
      type="file"
      accept="application/json,.json"
      class="sr-only"
      aria-label="Backup file"
      @change="chooseFile"
    />

    <div
      v-if="pending"
      role="note"
      class="space-y-2 rounded-lg border border-amber-300 bg-amber-50 p-3 dark:border-amber-800 dark:bg-amber-950/40"
    >
      <p class="text-sm">
        Backup of
        <span class="font-mono">{{ pending.summary.hostname || 'an unnamed router' }}</span>
        taken {{ new Date(pending.summary.createdAt).toLocaleString() }}
        <template v-if="pending.summary.ostiole"> by Ostiole {{ pending.summary.ostiole }}</template
        >.
        <template v-if="pending.summary.note">“{{ pending.summary.note }}”</template>
      </p>
      <p class="text-sm text-neutral-600 dark:text-neutral-400">
        {{ count(pending.summary.zones, 'zone') }},
        {{ count(pending.summary.interfaces, 'interface') }},
        {{ count(pending.summary.rules, 'rule') }},
        {{ count(pending.summary.aliases, 'alias', 'aliases') }},
        {{ count(pending.summary.gateways, 'gateway') }}.
        <template v-if="pending.summary.users">
          Also {{ count(pending.summary.users, 'account') }}, which only
          <span class="font-mono">ostiole restore --with-users</span> restores.
        </template>
      </p>
      <div>
        <p class="mb-1 text-sm font-medium">What it would change:</p>
        <ChangeList
          :changes="pending.changes"
          :limit="12"
          empty-label="Nothing: this backup matches the saved configuration."
        />
      </div>
      <div class="flex gap-2 pt-1">
        <button type="button" class="btn-primary" @click="loadIntoDraft">Load into draft</button>
        <button type="button" class="btn-secondary" @click="pending = null">Cancel</button>
      </div>
    </div>
  </section>
</template>
