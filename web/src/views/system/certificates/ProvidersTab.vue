<script setup>
import { Plus } from 'lucide-vue-next'
import { ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useConfigStore } from '@/stores/config'
import ProviderDialog from '@/views/system/certificates/ProviderDialog.vue'

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
      intro="Where a dns-01 challenge writes its record. The credentials are in the configuration."
      flush
    >
      <template #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add provider
        </button>
      </template>
      <div v-if="load.error.value" class="card-strip">
        <p role="alert" class="text-bad">{{ load.error.value }}</p>
      </div>
      <table class="table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Kind</th>
            <th>Used by</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!config.dnsProviders.length" key="empty" class="row-static">
            <td colspan="4" class="text-ink-muted">No providers.</td>
          </tr>
          <tr
            v-for="p in config.dnsProviders"
            :key="p.id"
            :class="{ 'row-changed': config.isChanged('acme.providers', p.id) }"
          >
            <td>
              <div class="font-mono text-code">{{ p.id }}</div>
              <div v-if="p.description" class="text-ink-muted">{{ p.description }}</div>
            </td>
            <td>{{ labelOf(p.kind) }}</td>
            <td class="font-mono text-code">
              {{ config.providerDependents(p.id).join(', ') || '—' }}
            </td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="edit(p)">Edit</button>
              <ConfirmButton
                v-if="config.providerDependents(p.id).length === 0"
                class="ml-3"
                label="Delete"
                :question="`Delete DNS provider ${p.id}?`"
                description="The credentials go with it."
                :typed="p.id"
                @confirm="config.removeDnsProvider(p.id)"
              />
              <span
                v-else
                class="ml-3 text-sm text-ink-muted"
                :title="`In use by ${config.providerDependents(p.id).join(', ')}`"
                >In use</span
              >
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <ProviderDialog v-model:open="open" :provider="editing" :kinds="kinds" />
  </div>
</template>
