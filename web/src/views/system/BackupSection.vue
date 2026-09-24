<script setup>
import { Download, LoaderCircle, Upload } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import RestorePreview from '@/views/system/RestorePreview.vue'

const auth = useAuthStore()
const config = useConfigStore()
const confirm = useConfirmStore()
const note = ref('')
const withUsers = ref(false)
const passphrase = ref('')
const redact = ref(false)
const restorePassphrase = ref('')
const pending = ref(null)
const fileInput = ref(null)

const download = useAsync(async () => {
  const { blob, name } = await api.config.backup({
    users: withUsers.value && !redact.value,
    note: note.value,
    passphrase: passphrase.value,
    redact: redact.value,
  })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
})

const restore = useAsync(async (file) => {
  pending.value = null
  pending.value = await api.config.restore(file, restorePassphrase.value)
})

const busy = computed(() => download.busy.value || restore.busy.value)

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
</script>

<template>
  <div class="space-y-5">
    <SectionCard v-if="!auth.readOnly" title="Backup" :locked="!auth.isAdmin">
      <div class="space-y-4">
        <p v-if="auth.isOperator" class="text-ink-muted">Only an admin can download a backup.</p>
        <p v-if="download.error.value" role="alert" class="text-bad">
          {{ download.error.value }}
        </p>
        <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
          <FormField id="bk-note" label="Description" hint="Stored in the file.">
            <input id="bk-note" v-model="note" class="input" placeholder="before the VLAN change" />
          </FormField>
          <FormField
            id="bk-passphrase"
            label="Passphrase"
            hint="Empty writes plain JSON anyone can read."
          >
            <input
              id="bk-passphrase"
              v-model="passphrase"
              type="password"
              class="input"
              autocomplete="new-password"
            />
          </FormField>
        </div>
        <ToggleRow
          id="bk-redact"
          v-model="redact"
          label="Leave secrets out"
          hint="For sharing. The draft asks for them on restore."
        />
        <ToggleRow
          id="bk-users"
          v-model="withUsers"
          :disabled="redact"
          label="Include administrator accounts"
          hint="Their password hashes go in the file."
        />
        <div>
          <button
            type="button"
            class="btn-primary"
            :disabled="busy"
            :aria-busy="download.busy.value"
            @click="download.run"
          >
            <LoaderCircle
              v-if="download.busy.value"
              class="size-4 animate-spin"
              aria-hidden="true"
            />
            <Download v-else class="size-4" aria-hidden="true" />
            Download backup
          </button>
        </div>
      </div>
    </SectionCard>

    <SectionCard v-if="!auth.readOnly" title="Restore">
      <div class="space-y-4">
        <p v-if="restore.error.value" role="alert" class="text-bad">{{ restore.error.value }}</p>
        <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
          <FormField
            id="bk-restore-passphrase"
            label="Passphrase"
            hint="Only for an encrypted file."
          >
            <input
              id="bk-restore-passphrase"
              v-model="restorePassphrase"
              type="password"
              class="input"
              autocomplete="off"
            />
          </FormField>
        </div>
        <div>
          <button
            type="button"
            class="btn-secondary"
            :disabled="busy"
            :aria-busy="restore.busy.value"
            @click="fileInput?.click()"
          >
            <LoaderCircle
              v-if="restore.busy.value"
              class="size-4 animate-spin"
              aria-hidden="true"
            />
            <Upload v-else class="size-4" aria-hidden="true" />
            Restore from file
          </button>
        </div>
        <input
          ref="fileInput"
          type="file"
          accept="application/json,.json,.age"
          class="sr-only"
          aria-label="Backup file"
          @change="chooseFile"
        />

        <RestorePreview
          v-if="pending"
          :pending="pending"
          @load="loadIntoDraft"
          @cancel="pending = null"
        />
      </div>
    </SectionCard>
  </div>
</template>
