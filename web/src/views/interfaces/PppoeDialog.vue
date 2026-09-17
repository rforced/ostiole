<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** Live links the session could run over. */
  candidates: { type: Array, default: () => [] },
  /** The session being edited, or null for a new one. */
  iface: { type: Object, default: null },
  /** Whether pppd and its unit are installed on the box. */
  ready: { type: Boolean, default: true },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const error = ref('')
const form = ref(blank())

function blank() {
  return {
    name: '',
    description: '',
    zone: '',
    parent: '',
    username: '',
    password: '',
    serviceName: '',
    acName: '',
    ipv6: false,
    lcpInterval: 10,
    lcpFailures: 5,
    mtu: 1492,
  }
}

const suggestedName = computed(() => {
  for (let i = 0; i < 100; i++) {
    if (!config.findInterface(`ppp${i}`)) return `ppp${i}`
  }
  return 'ppp0'
})

/** Links that are free to carry a session. */
const parents = computed(() => {
  const taken = new Map()
  for (const i of config.interfaces) {
    if (i.name === form.value.name) continue
    if (i.pppoe?.parent) taken.set(i.pppoe.parent, i.name)
    for (const m of i.bridge?.members ?? i.bond?.members ?? []) taken.set(m, i.name)
  }
  return props.candidates
    .filter((c) => !['loopback', 'wireguard', 'ppp'].includes(c.kind))
    .map((c) => ({
      name: c.name,
      takenBy: taken.get(c.name) ?? '',
      zone: config.findInterface(c.name)?.zone ?? '',
    }))
})

watch(
  () => [open.value, props.iface],
  () => {
    if (!open.value) return
    error.value = ''
    const i = props.iface
    if (!i) {
      form.value = { ...blank(), name: suggestedName.value }
      return
    }
    form.value = {
      ...blank(),
      name: i.name,
      description: i.description ?? '',
      zone: i.zone ?? '',
      mtu: i.mtu || 1492,
      ...i.pppoe,
      lcpInterval: i.pppoe?.lcpInterval || 10,
      lcpFailures: i.pppoe?.lcpFailures || 5,
    }
  },
  { immediate: true },
)

function save() {
  error.value = ''
  const f = form.value
  if (!f.parent) {
    error.value = 'Choose the interface the line is plugged into.'
    return
  }
  if (!props.iface && config.findInterface(f.name)) {
    error.value = `${f.name} is already configured.`
    return
  }
  const iface = {
    name: f.name,
    enabled: true,
    ...(props.iface ? { enabled: props.iface.enabled } : {}),
    ipv4: { mode: 'ppp' },
    ipv6: { mode: f.ipv6 ? 'ppp' : 'none' },
    pppoe: {
      parent: f.parent,
      username: f.username.trim(),
      password: f.password,
      ipv6: f.ipv6,
      lcpInterval: Number(f.lcpInterval) || 0,
      lcpFailures: Number(f.lcpFailures) || 0,
    },
  }
  if (f.zone) iface.zone = f.zone
  if (f.description) iface.description = f.description
  if (f.serviceName) iface.pppoe.serviceName = f.serviceName.trim()
  if (f.acName) iface.pppoe.acName = f.acName.trim()
  if (Number(f.mtu) && Number(f.mtu) !== 1492) iface.mtu = Number(f.mtu)
  // The Ethernet underneath becomes a port: the session holds the address.
  const parent = config.findInterface(f.parent)
  if (parent) {
    config.upsertInterface({
      ...parent,
      zone: '',
      ipv4: { mode: 'none' },
      ipv6: { mode: 'none' },
    })
  }
  config.upsertInterface(iface)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="iface ? `PPPoE ${iface.name}` : 'New PPPoE session'"
    description="The address, the default route, and the DNS servers come from the other end."
  >
    <form class="space-y-4" @submit.prevent="save">
      <p
        v-if="!ready"
        role="note"
        class="rounded-md border border-amber-300 bg-amber-50 p-2 text-sm dark:border-amber-800 dark:bg-amber-950/40"
      >
        PPPoE is not set up on this box yet. Run
        <span class="font-mono">ostiole services setup --with-pppoe</span> once as root, or the
        apply will fail.
      </p>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="ppp-name" label="Name">
          <input
            id="ppp-name"
            v-model="form.name"
            class="input font-mono"
            :disabled="!!iface"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="ppp-desc" label="Description">
          <input id="ppp-desc" v-model="form.description" class="input" />
        </FormField>
        <FormField id="ppp-parent" label="Plugged into">
          <select id="ppp-parent" v-model="form.parent" class="input" required>
            <option value="" disabled>Choose</option>
            <option
              v-for="p in parents"
              :key="p.name"
              :value="p.name"
              :disabled="!!p.takenBy && p.takenBy !== form.name"
            >
              {{ p.name }}{{ p.takenBy ? ` (already in ${p.takenBy})` : '' }}
            </option>
          </select>
        </FormField>
        <FormField id="ppp-zone" label="Zone">
          <select id="ppp-zone" v-model="form.zone" class="input">
            <option value="">Unassigned (traffic dropped)</option>
            <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
          </select>
        </FormField>
        <FormField id="ppp-user" label="Username">
          <input
            id="ppp-user"
            v-model="form.username"
            class="input font-mono"
            required
            spellcheck="false"
            autocomplete="off"
          />
        </FormField>
        <FormField id="ppp-pass" label="Password">
          <input
            id="ppp-pass"
            v-model="form.password"
            type="password"
            class="input font-mono"
            required
            autocomplete="new-password"
          />
        </FormField>
      </div>

      <details class="text-sm">
        <summary class="cursor-pointer select-none">Settings most lines do not need</summary>
        <div class="mt-3 grid gap-4 sm:grid-cols-2">
          <FormField
            id="ppp-service"
            label="Service name"
            hint="Only when the line offers several."
          >
            <input id="ppp-service" v-model="form.serviceName" class="input font-mono" />
          </FormField>
          <FormField
            id="ppp-ac"
            label="Concentrator name"
            hint="Only when the line offers several."
          >
            <input id="ppp-ac" v-model="form.acName" class="input font-mono" />
          </FormField>
          <FormField
            id="ppp-mtu"
            label="MTU"
            hint="1492 is what PPPoE leaves of an Ethernet frame."
          >
            <input
              id="ppp-mtu"
              v-model.number="form.mtu"
              type="number"
              min="576"
              max="1500"
              class="input w-32 font-mono"
            />
          </FormField>
          <FormField
            id="ppp-lcp"
            label="Echo every (seconds)"
            hint="How often the line is checked."
          >
            <input
              id="ppp-lcp"
              v-model.number="form.lcpInterval"
              type="number"
              min="0"
              max="3600"
              class="input w-32 font-mono"
            />
          </FormField>
          <FormField
            id="ppp-fail"
            label="Redial after missed echoes"
            hint="How many go unanswered before the session is dropped and redialled."
          >
            <input
              id="ppp-fail"
              v-model.number="form.lcpFailures"
              type="number"
              min="0"
              max="100"
              class="input w-32 font-mono"
            />
          </FormField>
        </div>
      </details>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.ipv6" type="checkbox" class="size-4 rounded border-neutral-300" />
        Ask for IPv6 as well
      </label>

      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
