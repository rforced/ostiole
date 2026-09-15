<script setup>
import { Plus } from 'lucide-vue-next'
import { onMounted, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import { newId } from '@/lib/ids'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const open = ref(false)
const editing = ref(null)
const form = ref(blank())

function blank() {
  return { id: '', description: '', enabled: true, destination: '', gateway: '', interface: '' }
}

onMounted(() => config.load())

watch(
  () => [open.value, editing.value],
  () => {
    if (open.value) form.value = editing.value ? { ...blank(), ...editing.value } : blank()
  },
)

function add() {
  editing.value = null
  open.value = true
}
function edit(r) {
  editing.value = r
  open.value = true
}
function save() {
  const f = form.value
  const out = {
    id: f.id || newId('rt'),
    enabled: f.enabled,
    destination: f.destination.trim(),
    gateway: f.gateway.trim(),
  }
  if (f.description) out.description = f.description
  if (f.interface) out.interface = f.interface
  config.upsertRoute(out)
  open.value = false
}
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">Routing</h1>
    <p v-if="config.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ config.error }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-neutral-500">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>
    <template v-else-if="config.draft">
      <p class="text-sm text-neutral-500">
        Default gateways come from interface settings (DHCP or a static gateway). Static routes go
        here.
      </p>
      <button type="button" class="btn-secondary" @click="add">
        <Plus class="mr-1 size-4" aria-hidden="true" /> Add static route
      </button>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
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
              <td colspan="5" class="text-neutral-500">No static routes.</td>
            </tr>
            <tr v-for="r in config.routes" :key="r.id" :class="{ 'opacity-50': !r.enabled }">
              <td class="font-mono text-xs">{{ r.destination }}</td>
              <td class="font-mono text-xs">{{ r.gateway }}</td>
              <td class="font-mono text-xs">{{ r.interface ?? 'auto' }}</td>
              <td>{{ r.description }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="edit(r)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  confirm-label="Delete route?"
                  @confirm="config.removeRoute(r.id)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <AppDialog v-model:open="open" :title="editing ? `Route ${editing.id}` : 'New static route'">
      <form class="space-y-4" @submit.prevent="save">
        <FormField id="rt-desc" label="Description">
          <input id="rt-desc" v-model="form.description" class="input" />
        </FormField>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField id="rt-dest" label="Destination network">
            <input
              id="rt-dest"
              v-model="form.destination"
              class="input font-mono"
              placeholder="10.200.0.0/16"
              required
              spellcheck="false"
            />
          </FormField>
          <FormField id="rt-gw" label="Gateway">
            <input
              id="rt-gw"
              v-model="form.gateway"
              class="input font-mono"
              placeholder="10.10.0.254"
              required
              spellcheck="false"
            />
          </FormField>
        </div>
        <FormField
          id="rt-if"
          label="Interface"
          hint="Optional; otherwise chosen from the gateway's network."
        >
          <select id="rt-if" v-model="form.interface" class="input">
            <option value="">Automatic</option>
            <option v-for="i in config.interfaces" :key="i.name" :value="i.name">
              {{ i.name }}
            </option>
          </select>
        </FormField>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
          Enabled
        </label>
        <div class="flex justify-end gap-2 pt-2">
          <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
          <button type="submit" class="btn-primary">Save to draft</button>
        </div>
      </form>
    </AppDialog>
  </div>
</template>
