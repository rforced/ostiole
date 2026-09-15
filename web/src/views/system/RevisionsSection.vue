<script setup>
import { onMounted, ref } from 'vue'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const revisions = ref([])
const error = ref('')
const loadedId = ref('')

onMounted(refresh)

async function refresh() {
  try {
    revisions.value = await api.config.revisions()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function loadIntoDraft(id) {
  error.value = ''
  try {
    config.replaceDraft(await api.config.revision(id))
    loadedId.value = id
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

defineExpose({ refresh })
</script>

<template>
  <section class="card space-y-3" aria-labelledby="rev-title">
    <h2 id="rev-title" class="card-title">Configuration history</h2>
    <p class="text-sm text-neutral-500">
      Every confirmed apply archives the previous configuration. Loading one into the draft lets you
      review it and roll back through the normal apply and confirm flow.
    </p>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="loadedId" role="status" class="text-sm text-amber-700 dark:text-amber-300">
      Revision <span class="font-mono">{{ loadedId }}</span> is now the draft. Apply it to roll
      back, or discard.
    </p>
    <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
      <table class="table">
        <thead>
          <tr>
            <th>Archived</th>
            <th>ID</th>
            <th>Size</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="revisions.length === 0">
            <td colspan="4" class="text-neutral-500">No archived revisions yet.</td>
          </tr>
          <tr v-for="r in revisions" :key="r.id">
            <td>{{ new Date(r.time).toLocaleString() }}</td>
            <td class="font-mono text-xs">{{ r.id }}</td>
            <td class="font-mono text-xs">{{ r.size }} B</td>
            <td class="text-right">
              <button type="button" class="link" @click="loadIntoDraft(r.id)">
                Load into draft
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
