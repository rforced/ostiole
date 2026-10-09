<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import AppNotice from '@/components/AppNotice.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** Live links the session could run over. */
  candidates: { type: Array, default: () => [] },
  /** The session being edited, or null for a new one. */
  iface: { type: Object, default: null },
  /** Whether pppd and its unit are installed on the router. */
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
    lcpInterval: '',
    lcpFailures: '',
    mtu: '',
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
      mtu: i.mtu ?? '',
      ...i.pppoe,
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
    pppoe: { parent: f.parent, username: f.username.trim(), password: f.password },
  }
  if (f.zone) iface.zone = f.zone
  if (f.description) iface.description = f.description
  if (f.serviceName) iface.pppoe.serviceName = f.serviceName.trim()
  if (f.acName) iface.pppoe.acName = f.acName.trim()
  if (f.ipv6) iface.pppoe.ipv6 = true
  if (Number(f.lcpInterval)) iface.pppoe.lcpInterval = Number(f.lcpInterval)
  if (Number(f.lcpFailures)) iface.pppoe.lcpFailures = Number(f.lcpFailures)
  if (Number(f.mtu)) iface.mtu = Number(f.mtu)
  // The Ethernet underneath becomes a port: the session holds the address.
  const parent = config.findInterface(f.parent)
  if (parent) {
    const port = { ...parent, ipv4: { mode: 'none' }, ipv6: { mode: 'none' } }
    delete port.zone
    config.upsertInterface(port)
  }
  config.upsertInterface(iface)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="iface ? `PPPoE ${iface.name}` : 'Add PPPoE session'"
    description="The address, the default route, and the DNS servers come from the other end."
  >
    <form id="pppoe-form" class="space-y-4" @submit.prevent="save">
      <AppNotice v-if="!ready">
        PPPoE is not set up on this router yet. Run
        <span class="font-mono">ostiole repair</span> once as root, or the apply will fail.
      </AppNotice>

      <div class="fields">
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

      <fieldset class="field-group">
        <legend>Line</legend>
        <div class="fields">
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
              placeholder="1492"
              class="input w-32 font-mono max-sm:w-full"
            />
          </FormField>
          <FormField id="ppp-lcp" label="Echo every (seconds)" hint="10 is the default.">
            <input
              id="ppp-lcp"
              v-model.number="form.lcpInterval"
              type="number"
              min="1"
              max="3600"
              placeholder="10"
              class="input w-32 font-mono max-sm:w-full"
            />
          </FormField>
          <FormField
            id="ppp-fail"
            label="Redial after missed echoes"
            hint="5 in a row is the default."
          >
            <input
              id="ppp-fail"
              v-model.number="form.lcpFailures"
              type="number"
              min="1"
              max="100"
              placeholder="5"
              class="input w-32 font-mono max-sm:w-full"
            />
          </FormField>
        </div>
      </fieldset>

      <ToggleRow v-model="form.ipv6" label="Ask for IPv6 as well" />

      <ErrorLine v-if="error" class="text-sm">{{ error }}</ErrorLine>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="pppoe-form" class="btn-primary">Save to draft</button>
    </template>
  </AppDialog>
</template>
