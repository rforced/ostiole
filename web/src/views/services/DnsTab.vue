<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const dns = computed(() => config.ensureServices().dns)

const upstreams = computed({
  get: () => (dns.value.upstreams ?? []).join(', '),
  set: (v) => {
    const list = parseList(v)
    if (list.length) dns.value.upstreams = list
    else delete dns.value.upstreams
  },
})
const domain = computed({
  get: () => dns.value.domain ?? '',
  set: (v) => {
    if (v.trim()) dns.value.domain = v.trim()
    else delete dns.value.domain
  },
})

const listenAll = computed({
  get: () => !(dns.value.interfaces ?? []).length,
  set: (all) => {
    if (all) delete dns.value.interfaces
    else
      dns.value.interfaces = config.interfaces.filter((i) => i.zone && i.enabled).map((i) => i.name)
  },
})

function toggleInterface(name, on) {
  const list = new Set(dns.value.interfaces ?? [])
  if (on) list.add(name)
  else list.delete(name)
  dns.value.interfaces = [...list]
}

const editing = ref(null)
const open = ref(false)
const form = ref({ hostname: '', ip: '', description: '' })
watch(
  () => [open.value, editing.value],
  () => {
    if (open.value) form.value = { hostname: '', ip: '', description: '', ...(editing.value ?? {}) }
  },
)
function add() {
  editing.value = null
  open.value = true
}
function edit(h) {
  editing.value = h
  open.value = true
}
function save() {
  const out = { hostname: form.value.hostname.trim(), ip: form.value.ip.trim() }
  if (form.value.description) out.description = form.value.description
  config.upsertHostOverride(out, editing.value?.hostname ?? out.hostname)
  open.value = false
}
</script>

<template>
  <div class="space-y-6">
    <label class="flex items-center gap-2 text-sm">
      <input v-model="dns.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
      <span class="font-medium">DNS service enabled</span>
      <span class="text-neutral-500"
        >— answers local names and forwards the rest upstream. This box uses it too.</span
      >
    </label>

    <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
      <FormField
        id="dns-up"
        label="Upstream resolvers"
        hint="Comma separated. Required when enabled (or set system DNS servers)."
      >
        <input
          id="dns-up"
          v-model="upstreams"
          class="input font-mono"
          spellcheck="false"
          placeholder="1.1.1.1, 9.9.9.9"
        />
      </FormField>
      <FormField id="dns-domain" label="Local domain" hint="Hosts get this suffix, e.g. lan.">
        <input id="dns-domain" v-model="domain" class="input font-mono" spellcheck="false" />
      </FormField>
    </div>

    <fieldset class="space-y-2 text-sm">
      <legend class="font-medium">Listen on</legend>
      <label class="flex items-center gap-2">
        <input v-model="listenAll" type="checkbox" class="size-4 rounded border-neutral-300" />
        Every interface outside external zones
      </label>
      <div v-if="!listenAll" class="ml-6 flex flex-wrap gap-4">
        <label
          v-for="i in config.interfaces.filter((x) => x.zone && x.enabled)"
          :key="i.name"
          class="flex items-center gap-2"
        >
          <input
            type="checkbox"
            class="size-4 rounded border-neutral-300"
            :checked="(dns.interfaces ?? []).includes(i.name)"
            @change="toggleInterface(i.name, $event.target.checked)"
          />
          <span class="font-mono">{{ i.name }}</span>
        </label>
      </div>
    </fieldset>

    <section class="space-y-3" aria-labelledby="hosts-title">
      <div class="flex items-center gap-3">
        <h2 id="hosts-title" class="font-medium">Host overrides</h2>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add host
        </button>
      </div>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Hostname</th>
              <th>IP</th>
              <th>Description</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!(dns.hostOverrides ?? []).length">
              <td colspan="4" class="text-neutral-500">
                No overrides. Static DHCP leases with hostnames resolve automatically.
              </td>
            </tr>
            <tr v-for="h in dns.hostOverrides" :key="h.hostname">
              <td class="font-mono text-xs">{{ h.hostname }}</td>
              <td class="font-mono text-xs">{{ h.ip }}</td>
              <td>{{ h.description }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="edit(h)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  confirm-label="Delete host?"
                  @confirm="config.removeHostOverride(h.hostname)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <AppDialog
      v-model:open="open"
      :title="editing ? `Host ${editing.hostname}` : 'New host override'"
    >
      <form class="space-y-4" @submit.prevent="save">
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField id="ho-name" label="Hostname">
            <input
              id="ho-name"
              v-model="form.hostname"
              class="input font-mono"
              required
              spellcheck="false"
            />
          </FormField>
          <FormField id="ho-ip" label="IP address">
            <input
              id="ho-ip"
              v-model="form.ip"
              class="input font-mono"
              required
              spellcheck="false"
            />
          </FormField>
        </div>
        <FormField id="ho-desc" label="Description">
          <input id="ho-desc" v-model="form.description" class="input" />
        </FormField>
        <div class="flex justify-end gap-2 pt-2">
          <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
          <button type="submit" class="btn-primary">Save to draft</button>
        </div>
      </form>
    </AppDialog>
  </div>
</template>
