<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import RouteDialog from '@/views/services/proxy/RouteDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const editing = ref(null)
const dialogOpen = ref(false)

const routes = computed(() => config.proxy.routes ?? [])

function add() {
  editing.value = null
  dialogOpen.value = true
}

function edit(route) {
  editing.value = route
  dialogOpen.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Routes" :count="routes.length" flush>
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add route
        </button>
      </template>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Route</th>
            <th>Port</th>
            <th>Server names</th>
            <th>Upstreams</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!routes.length">
            <td colspan="5" class="text-ink-muted">No routes.</td>
          </tr>
          <tr
            v-for="r in routes"
            :key="r.id"
            :class="{ 'row-changed': config.isChanged('services.proxy.routes', r.id) }"
          >
            <td data-label="">
              <div class="font-mono font-medium">
                {{ r.id
                }}<span v-if="!r.enabled" class="ml-1 font-sans text-ink-muted">(disabled)</span>
              </div>
              <div v-if="r.description" class="text-xs text-ink-muted">{{ r.description }}</div>
            </td>
            <td class="font-mono text-code" data-label="Port">{{ r.protocol }}/{{ r.port }}</td>
            <td class="font-mono text-code" data-label="Server names">
              <span v-if="(r.sni ?? []).length">{{ r.sni.join(', ') }}</span>
              <span v-else class="font-sans text-ink-muted">the whole port</span>
            </td>
            <td class="font-mono text-code" data-label="Upstreams">
              {{ (r.upstreams ?? []).map((u) => u.address).join(', ') }}
            </td>
            <td class="text-right whitespace-nowrap" data-label="">
              <button type="button" class="link" @click="edit(r)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete route ${r.id}?`"
                :description="`${r.protocol} port ${r.port}`"
                :typed="r.id"
                @confirm="config.removeProxyRoute(r.id)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <RouteDialog v-model:open="dialogOpen" :route="editing" />
  </div>
</template>
