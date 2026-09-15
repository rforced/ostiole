<script setup>
import { ref } from 'vue'

import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const iface = ref(config.interfaces[0]?.name ?? '')
const seconds = ref(10)
const count = ref(200)
const address = ref('')
const port = ref('')
const busy = ref(false)
const error = ref('')
const last = ref(null)

async function run() {
  busy.value = true
  error.value = ''
  try {
    const { blob, name } = await api.diagnostics.capture({
      interface: iface.value,
      seconds: Number(seconds.value) || 10,
      count: Number(count.value) || 0,
      address: address.value.trim(),
      port: Number(port.value) || 0,
    })
    // Hand the file to the browser: the API is behind a CSRF header, so a
    // plain download link cannot fetch it.
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.click()
    URL.revokeObjectURL(url)
    last.value = { name, size: blob.size }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <p class="max-w-3xl text-sm text-neutral-500">
      Records frames straight off the interface into a pcap file your browser downloads, ready for
      Wireshark. The capture stops at whichever limit comes first.
    </p>
    <form class="flex flex-wrap items-end gap-4" @submit.prevent="run">
      <FormField id="cp-if" label="Interface">
        <select id="cp-if" v-model="iface" class="input w-48" required>
          <option v-for="i in config.interfaces" :key="i.name" :value="i.name">{{ i.name }}</option>
        </select>
      </FormField>
      <FormField id="cp-secs" label="Seconds">
        <input
          id="cp-secs"
          v-model="seconds"
          type="number"
          min="1"
          max="120"
          class="input w-24 font-mono"
        />
      </FormField>
      <FormField id="cp-count" label="Packets">
        <input
          id="cp-count"
          v-model="count"
          type="number"
          min="1"
          max="5000"
          class="input w-28 font-mono"
        />
      </FormField>
      <FormField id="cp-addr" label="Address" hint="Optional; source or destination.">
        <input id="cp-addr" v-model="address" class="input w-44 font-mono" spellcheck="false" />
      </FormField>
      <FormField id="cp-port" label="Port" hint="Optional.">
        <input
          id="cp-port"
          v-model="port"
          type="number"
          min="1"
          max="65535"
          class="input w-28 font-mono"
        />
      </FormField>
      <button type="submit" class="btn-primary" :disabled="busy || !iface">
        {{ busy ? 'Capturing…' : 'Capture' }}
      </button>
    </form>

    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="last" class="text-sm text-neutral-500">
      Downloaded <span class="font-mono">{{ last.name }}</span> ({{ formatBytes(last.size) }}).
    </p>
  </div>
</template>
