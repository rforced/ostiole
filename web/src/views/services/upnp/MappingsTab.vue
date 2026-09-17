<script setup>
import { onMounted, ref } from 'vue'

import RefreshButton from '@/components/RefreshButton.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'

const mappings = ref([])

const load = useAsync(async () => {
  mappings.value = await api.upnp.mappings()
})
onMounted(load.run)
</script>

<template>
  <div class="space-y-3">
    <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    <p v-if="load.error.value" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ load.error.value }}
    </p>
    <p class="text-sm text-neutral-500">
      The list is empty for a moment after an apply, while clients re-open what they had.
    </p>
    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>Protocol</th>
            <th>External port</th>
            <th>Client</th>
            <th>Internal port</th>
            <th>Expires</th>
            <th>Description</th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!mappings.length" key="empty" class="row-static">
            <td colspan="6" class="text-neutral-500">
              {{ load.updatedAt.value ? 'No mappings.' : 'Reading mappings…' }}
            </td>
          </tr>
          <tr
            v-for="m in mappings"
            :key="`${m.protocol}:${m.externalPort}:${m.internal}:${m.internalPort}`"
          >
            <td>
              <span class="badge">{{ m.protocol }}</span>
            </td>
            <td class="font-mono text-code">{{ m.externalPort }}</td>
            <td class="font-mono text-code">{{ m.internal }}</td>
            <td class="font-mono text-code">{{ m.internalPort }}</td>
            <td>
              {{ m.expires ? new Date(m.expires).toLocaleString() : 'never' }}
            </td>
            <td>{{ m.description }}</td>
          </tr>
        </TransitionGroup>
      </table>
    </div>
  </div>
</template>
