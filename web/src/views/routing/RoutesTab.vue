<script setup>
import { Plus } from '@lucide/vue'
import { ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import StaticRouteDialog from '@/views/routing/StaticRouteDialog.vue'

defineProps({
  replies: { type: Array, required: true },
})

const auth = useAuthStore()
const config = useConfigStore()
const open = ref(false)
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
    <SectionCard title="Static routes" :count="config.routes.length" flush>
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add static route
        </button>
      </template>
      <table class="table">
        <thead>
          <tr>
            <th>Destination</th>
            <th>Gateway</th>
            <th>Interface</th>
            <th>Description</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="config.routes.length === 0">
            <td colspan="5" class="text-ink-muted">No static routes.</td>
          </tr>
          <tr
            v-for="r in config.routes"
            :key="r.id"
            :class="{ 'opacity-50': !r.enabled, 'row-changed': config.isChanged('routes', r.id) }"
          >
            <td class="font-mono text-code">{{ r.destination }}</td>
            <td class="font-mono text-code">{{ r.gateway }}</td>
            <td class="font-mono text-code">{{ r.interface ?? 'auto' }}</td>
            <td>{{ r.description }}</td>
            <td class="actions">
              <button type="button" class="link-action" @click="edit(r)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                label="Delete"
                :question="`Delete route ${r.id}?`"
                :description="r.description"
                @confirm="config.removeRoute(r.id)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <SectionCard
      v-if="replies.length"
      title="Replies"
      :count="replies.length"
      intro="Connections that come in on an external interface are answered out of it, whichever holds the default route."
      flush
    >
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Interface</th>
            <th>Table</th>
            <th>Next hop</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in replies" :key="r.interface">
            <td class="font-mono text-code" data-label="">{{ r.interface }}</td>
            <td class="font-mono text-code" data-label="Table">{{ r.table }}</td>
            <td class="font-mono text-code" data-label="Next hop">
              <template v-if="r.nextHops.length">{{ r.nextHops.join(', ') }}</template>
              <span v-else class="font-sans text-ink-muted">default route</span>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <StaticRouteDialog v-model:open="open" :route="editing" />
  </div>
</template>
