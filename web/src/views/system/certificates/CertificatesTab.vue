<script setup>
import { Download, LoaderCircle, Plus, RefreshCw } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'
import { useConfirmStore } from '@/stores/confirm'
import { useToastStore } from '@/stores/toast'
import CertificateDialog from '@/views/system/certificates/CertificateDialog.vue'

/** How often the list is re-read while an order is running. */
const ISSUING_POLL_MS = 5000

const config = useConfigStore()
const confirm = useConfirmStore()
const toast = useToastStore()

const page = ref({ builtIn: null, certificates: [], providerKinds: [] })
const actionError = ref('')
const load = useAsync(
  async () => {
    page.value = await api.certificates.list()
  },
  { immediate: true },
)

/** Status by id, so a row can be drawn from the draft and the server. */
const status = computed(() =>
  Object.fromEntries((page.value.certificates ?? []).map((c) => [c.id, c])),
)
const issuing = computed(() => (page.value.certificates ?? []).some((c) => c.running))
const poll = useAsync(load.run, { interval: ISSUING_POLL_MS, autostart: false })
watch(issuing, (on) => (on ? poll.start() : poll.stop()))
// An apply is what puts a certificate on the router, so the rows are
// worth re-reading the moment one lands.
watch(() => config.applied, load.run)

const error = computed(() => actionError.value || load.error.value)

const served = computed({
  get: () => config.draft?.system?.management?.certificate ?? '',
  set: (id) => config.setManagementCertificate(id),
})

const editing = ref(null)
const dialogOpen = ref(false)
function add() {
  editing.value = null
  dialogOpen.value = true
}
function edit(cert) {
  editing.value = cert
  dialogOpen.value = true
}

/** What a row says it is doing, from its files and its last attempt. */
function state(cert) {
  const st = status.value[cert.id]
  if (!st)
    return { tone: '', label: 'apply to issue', title: 'This certificate is only in the draft.' }
  if (st.running) return { tone: '', label: 'issuing' }
  if (st.expired) return { tone: 'badge-warn', label: 'expired' }
  if (!st.issued && st.lastError)
    return { tone: 'badge-warn', label: 'failed', title: st.lastError }
  if (!st.issued) return { tone: '', label: 'not issued' }
  if (st.expiresSoon) return { tone: 'badge-warn', label: 'expires soon', title: st.lastError }
  if (st.lastError) return { tone: 'badge-warn', label: 'issued', title: st.lastError }
  return { tone: 'badge-ok', label: 'issued' }
}

const expires = (cert) => {
  const at = status.value[cert.id]?.notAfter
  return at ? new Date(at).toLocaleDateString() : '—'
}

async function issue(cert) {
  actionError.value = ''
  try {
    await api.certificates.issue(cert.id)
    toast.show(`Ordering ${cert.id}. The CA takes a moment.`)
    // The re-read shows the row issuing, and that is what starts the poll;
    // an order that already failed shows its error and starts nothing.
    await load.run()
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}

const regenerate = useAsync(async () => {
  page.value.builtIn = await api.certificates.regenerate()
  toast.show('New self-signed certificate in place. Browsers warn about it once more.')
})

async function askRegenerate() {
  const ok = await confirm.ask({
    question: 'Regenerate the self-signed certificate?',
    description: 'Browsers warn about the new one, and its fingerprint is the one to check.',
    confirmLabel: 'Regenerate',
    typed: 'regenerate',
  })
  if (ok) await regenerate.run()
}

/** Names the router answers to that the built-in certificate misses. */
const missing = computed(() => {
  const built = page.value.builtIn
  if (!built) return []
  const covered = new Set(built.names ?? [])
  return (built.hosts ?? []).filter((h) => h && !covered.has(h))
})

const DOWNLOADS = [
  { name: 'fullchain.pem', label: 'Full chain' },
  { name: 'cert.pem', label: 'Certificate' },
  { name: 'chain.pem', label: 'Chain' },
  { name: 'key.pem', label: 'Private key' },
]

const pkcs12For = ref(null)
const pkcs12Password = ref('')
function askPKCS12(cert) {
  pkcs12For.value = cert
  pkcs12Password.value = ''
}

const downloadPKCS12 = useAsync(async () => {
  const { blob, name } = await api.certificates.pkcs12(pkcs12For.value.id, pkcs12Password.value)
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
  pkcs12For.value = null
})

/** Whether the files exist to download. */
const issued = (cert) => Boolean(status.value[cert.id]?.issued)

function remove(cert) {
  config.removeCertificate(cert.id)
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Web UI">
      <template #actions>
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run"
        />
        <button
          v-if="page.builtIn"
          type="button"
          class="btn-secondary"
          :disabled="regenerate.busy.value"
          :aria-busy="regenerate.busy.value"
          @click="askRegenerate"
        >
          <LoaderCircle
            v-if="regenerate.busy.value"
            class="size-4 animate-spin"
            aria-hidden="true"
          />
          <RefreshCw v-else class="size-4" aria-hidden="true" />
          {{ regenerate.busy.value ? 'Regenerating…' : 'Regenerate self-signed' }}
        </button>
      </template>

      <div class="space-y-4">
        <p v-if="error" role="alert" class="text-bad">{{ error }}</p>

        <template v-if="page.builtIn">
          <FormField id="served-by" label="Served by" hint="From the next connection.">
            <select id="served-by" v-model="served" class="input sm:w-96">
              <option value="">Built-in self-signed</option>
              <option v-for="c in config.certificates" :key="c.id" :value="c.id">{{ c.id }}</option>
            </select>
          </FormField>

          <fieldset>
            <legend class="group-title mb-1">Built-in self-signed</legend>
            <dl class="kv">
              <dt>Valid for</dt>
              <dd class="font-mono">{{ page.builtIn.names.join(', ') || '—' }}</dd>
              <dt>Issued by</dt>
              <dd>{{ page.builtIn.issuer }}</dd>
              <dt>Expires</dt>
              <dd>{{ new Date(page.builtIn.notAfter).toLocaleString() }}</dd>
              <dt>Fingerprint</dt>
              <dd class="font-mono text-code break-all">{{ page.builtIn.fingerprint }}</dd>
            </dl>
          </fieldset>

          <AppNotice v-if="missing.length">
            This router also answers to
            <span class="font-mono">{{ missing.join(', ') }}</span
            >, which the built-in certificate does not cover.
          </AppNotice>
        </template>
        <p v-else class="text-ink-muted">
          {{ load.updatedAt.value ? 'This server is not serving HTTPS.' : 'Reading…' }}
        </p>
      </div>
    </SectionCard>

    <SectionCard title="Certificates" :count="config.certificates.length" flush>
      <template #intro>
        Renewed hourly by
        <RouterLink to="/system/crons" class="link">system:certificates</RouterLink>. Other hosts
        fetch one with a token limited to it.
      </template>
      <template #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add certificate
        </button>
      </template>
      <table class="table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Covers</th>
            <th>Source</th>
            <th>Status</th>
            <th>Expires</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!config.certificates.length" key="empty" class="row-static">
            <td colspan="6" class="text-ink-muted">No certificates.</td>
          </tr>
          <tr
            v-for="c in config.certificates"
            :key="c.id"
            :class="{ 'row-changed': config.isChanged('certificates', c.id) }"
          >
            <td>
              <div class="font-mono text-code">{{ c.id }}</div>
              <div v-if="c.description" class="text-ink-muted">{{ c.description }}</div>
            </td>
            <td class="font-mono text-code">
              {{ (c.names ?? []).join(', ') }}
              <span v-if="(c.interfaceAddresses ?? []).length" class="text-ink-muted">
                {{ (c.interfaceAddresses ?? []).join(', ') }}
              </span>
            </td>
            <td>
              {{ c.source === 'uploaded' ? 'uploaded' : c.challenge || 'acme' }}
            </td>
            <td>
              <span class="badge" :class="state(c).tone" :title="state(c).title">
                {{ state(c).label }}
              </span>
            </td>
            <td class="whitespace-nowrap">{{ expires(c) }}</td>
            <td class="text-right whitespace-nowrap">
              <button
                v-if="c.source !== 'uploaded'"
                type="button"
                class="link"
                :disabled="status[c.id]?.running"
                :aria-busy="status[c.id]?.running === true"
                @click="issue(c)"
              >
                <LoaderCircle
                  v-if="status[c.id]?.running"
                  class="mr-1 inline size-4 animate-spin"
                  aria-hidden="true"
                />
                {{ status[c.id]?.running ? 'Issuing…' : 'Issue now' }}
              </button>
              <span v-if="issued(c)" class="ml-3 inline-flex items-center gap-2">
                <Download class="size-4 text-ink-muted" aria-hidden="true" />
                <a
                  v-for="d in DOWNLOADS"
                  :key="d.name"
                  class="link"
                  :href="api.certificates.fileURL(c.id, d.name)"
                  >{{ d.label }}</a
                >
                <button type="button" class="link" @click="askPKCS12(c)">PKCS#12</button>
              </span>
              <button type="button" class="link ml-3" @click="edit(c)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete certificate ${c.id}?`"
                description="Its files are deleted within a few seconds of the apply."
                :dependents="config.certificateDependents(c.id)"
                dependents-label="Goes back to the built-in certificate"
                :typed="c.id"
                @confirm="remove(c)"
              />
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <CertificateDialog v-model:open="dialogOpen" :certificate="editing" />

    <AppDialog
      :open="Boolean(pkcs12For)"
      title="Download as PKCS#12"
      description="Windows and Java read this one."
      @update:open="pkcs12For = null"
    >
      <form class="space-y-4" @submit.prevent="downloadPKCS12.run">
        <p v-if="downloadPKCS12.error.value" role="alert" class="text-bad">
          {{ downloadPKCS12.error.value }}
        </p>
        <FormField id="p12-password" label="Password" hint="Asked for when the file is opened.">
          <input
            id="p12-password"
            v-model="pkcs12Password"
            type="password"
            class="input"
            autocomplete="new-password"
          />
        </FormField>
        <div class="flex justify-end gap-2 pt-2">
          <button type="button" class="btn-secondary" @click="pkcs12For = null">Cancel</button>
          <button type="submit" class="btn-primary" :disabled="downloadPKCS12.busy.value">
            Download
          </button>
        </div>
      </form>
    </AppDialog>
  </div>
</template>
