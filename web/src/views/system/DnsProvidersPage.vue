<script setup>
import { Plus } from 'lucide-vue-next'
import { ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import SectionCard from '@/components/SectionCard.vue'
import UsedByCell from '@/components/UsedByCell.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { adminOnly, useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ProviderDialog from '@/views/system/providers/ProviderDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const kinds = ref([])
const load = useAsync(
  async () => {
    kinds.value = (await api.certificates.list()).providerKinds ?? []
  },
  { immediate: true },
)

const editing = ref(null)
const open = ref(false)

function add() {
  editing.value = null
  open.value = true
}
function edit(provider) {
  editing.value = provider
  open.value = true
}

const labelOf = (kind) => kinds.value.find((k) => k.kind === kind)?.label ?? kind
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="DNS providers"
      :count="config.dnsProviders.length"
      intro="Where certificates and dynamic DNS write their records. The credentials are in the configuration."
      flush
    >
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" :disabled="!auth.isAdmin" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add provider
        </button>
      </template>
      <div v-if="load.error.value" class="card-strip">
        <ErrorLine>{{ load.error.value }}</ErrorLine>
      </div>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Name</th>
            <th>Kind</th>
            <th>Domains</th>
            <th>Used by</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!config.dnsProviders.length">
            <td colspan="5" class="text-ink-muted">No providers.</td>
          </tr>
          <tr
            v-for="p in config.dnsProviders"
            :key="p.id"
            :class="{ 'row-changed': config.isChanged('dnsProviders', p.id) }"
          >
            <td data-label="">
              <div class="font-mono text-code">{{ p.id }}</div>
              <div v-if="p.description" class="text-xs text-ink-muted">{{ p.description }}</div>
            </td>
            <td data-label="Kind">{{ labelOf(p.kind) }}</td>
            <td class="font-mono text-code" data-label="Domains">
              {{ p.domains?.join(', ') || '—' }}
            </td>
            <UsedByCell :names="config.providerDependents(p.id)" />
            <td class="actions" data-label="">
              <button type="button" class="link-action" @click="edit(p)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                label="Delete"
                :question="`Delete DNS provider ${p.id}?`"
                description="The credentials go with it."
                :typed="p.id"
                :disabled="!auth.isAdmin || config.providerDependents(p.id).length > 0"
                @confirm="config.removeDnsProvider(p.id)"
              />
            </td>
          </tr>
        </tbody>
      </table>
      <template v-if="auth.isOperator" #footer>
        {{ adminOnly('add or delete', 'DNS providers, or change their kind and credentials') }}
      </template>
    </SectionCard>

    <ProviderDialog v-model:open="open" :provider="editing" :kinds="kinds" />
  </div>
</template>
