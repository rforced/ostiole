<script setup>
import { Plus } from 'lucide-vue-next'
import { ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { useConfigStore } from '@/stores/config'
import AliasDialog from '@/views/firewall/AliasDialog.vue'

const config = useConfigStore()
const editing = ref(null)
const open = ref(false)

function add() {
  editing.value = null
  open.value = true
}
function edit(a) {
  editing.value = a
  open.value = true
}
</script>

<template>
  <div class="space-y-3">
    <button type="button" class="btn-secondary" @click="add">
      <Plus class="mr-1 size-4" aria-hidden="true" /> Add alias
    </button>
    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>Alias</th>
            <th>Type</th>
            <th>Entries</th>
            <th>Description</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="config.aliases.length === 0">
            <td colspan="5" class="text-neutral-500">No aliases yet.</td>
          </tr>
          <tr v-for="a in config.aliases" :key="a.name">
            <td class="font-mono font-medium">{{ a.name }}</td>
            <td>{{ a.type }}</td>
            <td class="font-mono text-xs">
              {{ a.entries.slice(0, 4).join(', ')
              }}<span v-if="a.entries.length > 4"> … ({{ a.entries.length }})</span>
            </td>
            <td>{{ a.description }}</td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="edit(a)">Edit</button>
              <ConfirmButton
                v-if="config.aliasReferences(a.name).length === 0"
                class="ml-3"
                label="Delete"
                confirm-label="Delete alias?"
                @confirm="config.removeAlias(a.name)"
              />
              <span
                v-else
                class="ml-3 text-xs text-neutral-500"
                :title="config.aliasReferences(a.name).join(', ')"
                >in use</span
              >
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <AliasDialog v-model:open="open" :alias="editing" />
  </div>
</template>
