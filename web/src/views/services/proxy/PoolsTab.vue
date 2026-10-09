<script setup>
import { Plus } from '@lucide/vue'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import UsedByCell from '@/components/UsedByCell.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import PoolDialog from '@/views/services/proxy/PoolDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const editing = ref(null)
const dialogOpen = ref(false)

const pools = computed(() => config.proxy.pools ?? [])

function add() {
  editing.value = null
  dialogOpen.value = true
}

function edit(pool) {
  editing.value = pool
  dialogOpen.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Pools" :count="pools.length" flush>
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add pool
        </button>
      </template>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Pool</th>
            <th>Upstreams</th>
            <th>Balancing</th>
            <th>Health</th>
            <th>Used by</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!pools.length">
            <td colspan="6" class="text-ink-muted">No pools.</td>
          </tr>
          <tr
            v-for="p in pools"
            :key="p.id"
            :class="{ 'row-changed': config.isChanged('services.proxy.pools', p.id) }"
          >
            <td data-label="">
              <div class="font-mono font-medium">{{ p.id }}</div>
              <div v-if="p.description" class="text-xs text-ink-muted">{{ p.description }}</div>
            </td>
            <td class="font-mono text-code" data-label="Upstreams">
              {{ (p.upstreams ?? []).map((u) => u.address).join(', ') }}
              <span v-if="p.tls" class="ml-1 text-ink-muted">over TLS</span>
            </td>
            <td data-label="Balancing">{{ p.policy || 'round_robin' }}</td>
            <td data-label="Health">
              <template v-if="p.healthPath">
                {{ p.healthPath }} every {{ p.healthSeconds || 30 }}s
              </template>
              <span v-else class="text-ink-muted">none</span>
            </td>
            <UsedByCell :names="config.poolDependents(p.id)" />
            <td class="actions" data-label="">
              <button type="button" class="link-action" @click="edit(p)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                label="Delete"
                :question="`Delete pool ${p.id}?`"
                :typed="p.id"
                :disabled="config.poolDependents(p.id).length > 0"
                @confirm="config.removePool(p.id)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <PoolDialog v-model:open="dialogOpen" :pool="editing" />
  </div>
</template>
