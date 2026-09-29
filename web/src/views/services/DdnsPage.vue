<script setup>
import { LoaderCircle, Plus } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { providerFor } from '@/lib/providers'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import RecordDialog from '@/views/services/ddns/RecordDialog.vue'

/** How often the status is read while a record is waiting on its provider. */
const BUSY_POLL_MS = 2000

const auth = useAuthStore()
const config = useConfigStore()

const page = ref({ records: [], kinds: [] })
const actionError = ref('')
const load = useAsync(
  async () => {
    page.value = await api.ddns.status()
  },
  { immediate: true },
)
const error = computed(() => actionError.value || load.error.value)

/** Each applied record's types, by record id. */
const status = computed(() => {
  const out = {}
  for (const st of page.value.records ?? []) (out[st.id] ??= []).push(st)
  return out
})
const waiting = computed(() =>
  (page.value.records ?? []).some((s) => s.state === 'updating' || s.state === 'pending'),
)
const poll = useAsync(load.run, { interval: BUSY_POLL_MS, autostart: false })
watch(waiting, (on) => (on ? poll.start() : poll.stop()))
// An apply is what starts a record, so the rows are worth reading the
// moment one lands.
watch(() => config.applied, load.run)

/** Whether some provider can keep a record: one of a kind with a client, holding a domain. */
const canKeep = computed(() =>
  config.dnsProviders.some(
    (p) => (p.domains ?? []).length && (page.value.kinds ?? []).some((k) => k.kind === p.kind),
  ),
)
const kindNames = computed(() => (page.value.kinds ?? []).map((k) => k.label).join(', '))

const BADGES = {
  current: { tone: 'badge-ok', label: 'current' },
  pending: { tone: '', label: 'pending' },
  updating: { tone: '', label: 'updating' },
  failed: { tone: 'badge-bad', label: 'failed' },
  'no-address': { tone: 'badge-warn', label: 'no address' },
  off: { tone: '', label: 'off' },
}

/** A row's Address lines, one per record type, from the applied record's status. */
function lines(r) {
  const known = status.value[r.id]
  return [r.ipv4 && 'A', r.ipv6 && 'AAAA'].filter(Boolean).map((type) => {
    const st = known?.find((s) => s.type === type)
    if (!st) return { type, address: '', badge: { tone: '', label: 'apply to start' } }
    return {
      type,
      address: st.address || (st.published ?? []).join(', '),
      badge: BADGES[st.state] ?? { tone: '', label: st.state },
      error: st.error,
    }
  })
}

/** When the router last changed any of the record's types at the provider. */
function changed(r) {
  const at = (status.value[r.id] ?? [])
    .map((s) => s.changedAt)
    .filter(Boolean)
    .sort()
    .at(-1)
  return at ? new Date(at).toLocaleString() : '—'
}

const updating = (r) => (status.value[r.id] ?? []).some((s) => s.state === 'updating')

async function updateNow(r) {
  actionError.value = ''
  try {
    await api.ddns.update(r.id)
    await load.run()
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}

/**
 * The DNS provider that writes each record, found from its name, by record
 * id. Its kind is left out where the provider is named for it.
 */
const providers = computed(() => {
  const out = {}
  for (const r of config.ddnsRecords) {
    const p = providerFor(config.dnsProviders, r.name)?.provider
    if (!p) continue
    const label = (page.value.kinds ?? []).find((k) => k.kind === p.kind)?.label ?? ''
    out[r.id] = { id: p.id, kind: label.toLowerCase() === p.id.toLowerCase() ? '' : label }
  }
  return out
})

/** The provider's own name, for what a delete leaves behind. */
function where(r) {
  const p = providerFor(config.dnsProviders, r.name)?.provider
  return (page.value.kinds ?? []).find((k) => k.kind === p?.kind)?.label ?? 'the provider'
}

const open = ref(false)
/** The record the dialog edits; null adds one. */
const editing = ref(null)
function add() {
  editing.value = null
  open.value = true
}
function edit(r) {
  editing.value = r
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Records" :count="config.ddnsRecords.length" flush>
      <template #intro>
        Kept by
        <RouterLink to="/system/crons" class="link">system:ddns</RouterLink>, which writes to a
        provider only when an address changes.
      </template>
      <template #actions>
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run"
        />
        <button v-if="!auth.readOnly" type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add record
        </button>
      </template>
      <div v-if="error" class="card-strip">
        <p role="alert" class="text-bad">{{ error }}</p>
      </div>
      <div v-if="load.updatedAt.value && !canKeep" class="card-strip">
        <p class="text-sm">
          No DNS provider can keep a record yet. Add a {{ kindNames }} provider with its domain
          under <RouterLink to="/system/dns-providers" class="link">DNS providers</RouterLink>.
        </p>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Name</th>
            <th>DNS provider</th>
            <th>Interface</th>
            <th>Address</th>
            <th>Changed</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!config.ddnsRecords.length">
            <td colspan="6" class="text-ink-muted">No records.</td>
          </tr>
          <tr
            v-for="r in config.ddnsRecords"
            :key="r.id"
            :class="{ 'row-changed': config.isChanged('services.ddns.records', r.id) }"
          >
            <td data-label="">
              <div class="font-mono text-code">{{ r.name }}</div>
              <div v-if="r.description" class="text-xs text-ink-muted">{{ r.description }}</div>
            </td>
            <td data-label="DNS provider">
              <template v-if="providers[r.id]">
                <div class="font-mono text-code">{{ providers[r.id].id }}</div>
                <div v-if="providers[r.id].kind" class="text-xs text-ink-muted">
                  {{ providers[r.id].kind }}
                </div>
              </template>
              <template v-else>—</template>
            </td>
            <td class="font-mono" data-label="Interface">{{ r.interface }}</td>
            <td data-label="Address">
              <div v-for="l in lines(r)" :key="l.type">
                <span class="font-mono text-code">
                  <span class="text-ink-muted">{{ l.type }}</span
                  >&nbsp;{{ l.address || '—' }}
                </span>
                <span class="badge ml-1" :class="l.badge.tone">{{ l.badge.label }}</span>
                <div v-if="l.error" class="text-xs text-bad">{{ l.error }}</div>
              </div>
            </td>
            <td class="text-xs whitespace-nowrap" data-label="Changed">{{ changed(r) }}</td>
            <td class="text-right whitespace-nowrap" data-label="">
              <button
                v-if="!auth.readOnly"
                type="button"
                class="link"
                :disabled="!status[r.id] || !r.enabled || updating(r)"
                :aria-busy="updating(r)"
                @click="updateNow(r)"
              >
                <LoaderCircle
                  v-if="updating(r)"
                  class="inline size-4 animate-spin"
                  aria-hidden="true"
                />
                Update now
              </button>
              <button type="button" class="link ml-3" @click="edit(r)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete dynamic DNS record ${r.name}?`"
                :description="`The record at ${where(r)} stays as it is.`"
                @confirm="config.removeDdnsRecord(r)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
    <RecordDialog v-model:open="open" :record="editing" :kinds="page.kinds ?? []" />
  </div>
</template>
