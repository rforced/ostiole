<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** Interface config being edited, or a fresh one for an unconfigured link. */
  iface: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['saved'])

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return {
    name: '',
    description: '',
    zone: '',
    enabled: true,
    ipv4: { mode: 'none', address: '', gateway: '' },
    ipv6: {
      mode: 'none',
      address: '',
      gateway: '',
      prefixHint: '',
      delegatedFrom: '',
      subnetId: 0,
    },
    mtu: 0,
  }
}

watch(
  () => [open.value, props.iface],
  () => {
    if (!open.value) return
    const src = props.iface ?? blank()
    form.value = {
      ...blank(),
      ...JSON.parse(JSON.stringify(src)),
      ipv4: { mode: 'none', address: '', gateway: '', ...src.ipv4 },
      ipv6: {
        mode: 'none',
        address: '',
        gateway: '',
        prefixHint: '',
        delegatedFrom: '',
        subnetId: 0,
        ...src.ipv6,
      },
    }
  },
  { immediate: true },
)

const title = computed(() => `Interface ${form.value.name}`)

/** Interfaces that ask an upstream for a prefix, which this one can share. */
const upstreams = computed(() =>
  config.interfaces.filter((i) => i.name !== form.value.name && i.ipv6?.prefixHint),
)

function save() {
  const out = JSON.parse(JSON.stringify(form.value))
  if (out.ipv4.mode !== 'static') out.ipv4.address = ''
  if (out.ipv6.mode !== 'static') out.ipv6.address = ''
  if (out.ipv6.mode !== 'dhcp') out.ipv6.prefixHint = ''
  if (out.ipv6.mode !== 'delegated') {
    out.ipv6.delegatedFrom = ''
    out.ipv6.subnetId = 0
  }
  for (const fam of ['ipv4', 'ipv6']) {
    if (!out[fam].address) delete out[fam].address
    if (!out[fam].gateway) delete out[fam].gateway
  }
  if (!out.ipv6.prefixHint) delete out.ipv6.prefixHint
  if (!out.ipv6.delegatedFrom) delete out.ipv6.delegatedFrom
  out.ipv6.subnetId = Number(out.ipv6.subnetId) || 0
  if (!out.ipv6.subnetId) delete out.ipv6.subnetId
  if (!out.zone) delete out.zone
  if (!out.description) delete out.description
  if (!out.mtu) delete out.mtu
  config.upsertInterface(out)
  emit('saved', out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="title"
    description="Changes stay in the draft until you apply them."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="if-desc" label="Description">
          <input id="if-desc" v-model="form.description" class="input" />
        </FormField>
        <FormField id="if-zone" label="Zone">
          <select id="if-zone" v-model="form.zone" class="input">
            <option value="">Unassigned (traffic dropped)</option>
            <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
          </select>
        </FormField>
      </div>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
        Enabled
      </label>

      <fieldset class="space-y-3 rounded-md border border-neutral-200 p-3 dark:border-neutral-800">
        <legend class="px-1 text-sm font-medium">IPv4</legend>
        <FormField id="if-v4-mode" label="Mode">
          <select id="if-v4-mode" v-model="form.ipv4.mode" class="input">
            <option value="none">None</option>
            <option value="static">Static</option>
            <option value="dhcp">DHCP client</option>
          </select>
        </FormField>
        <div v-if="form.ipv4.mode === 'static'" class="grid gap-3 sm:grid-cols-2">
          <FormField id="if-v4-addr" label="Address (CIDR)">
            <input
              id="if-v4-addr"
              v-model="form.ipv4.address"
              class="input font-mono"
              placeholder="192.168.1.1/24"
              required
            />
          </FormField>
          <FormField id="if-v4-gw" label="Gateway" hint="Only for upstream links.">
            <input
              id="if-v4-gw"
              v-model="form.ipv4.gateway"
              class="input font-mono"
              placeholder="optional"
            />
          </FormField>
        </div>
      </fieldset>

      <fieldset class="space-y-3 rounded-md border border-neutral-200 p-3 dark:border-neutral-800">
        <legend class="px-1 text-sm font-medium">IPv6</legend>
        <FormField id="if-v6-mode" label="Mode">
          <select id="if-v6-mode" v-model="form.ipv6.mode" class="input">
            <option value="none">None</option>
            <option value="static">Static</option>
            <option value="slaac">SLAAC (router advertisements)</option>
            <option value="dhcp">DHCPv6</option>
            <option value="delegated" :disabled="!upstreams.length">
              Delegated (a subnet of an upstream prefix)
            </option>
          </select>
        </FormField>
        <FormField
          v-if="form.ipv6.mode === 'dhcp'"
          id="if-v6-hint"
          label="Ask for a prefix"
          hint="On a WAN: the size to request from the ISP, so the networks behind this box get real addresses. Leave empty to ask for nothing."
        >
          <input
            id="if-v6-hint"
            v-model="form.ipv6.prefixHint"
            class="input font-mono"
            placeholder="::/56"
            spellcheck="false"
          />
        </FormField>
        <div v-if="form.ipv6.mode === 'delegated'" class="grid gap-3 sm:grid-cols-2">
          <FormField id="if-v6-from" label="Prefix from">
            <select id="if-v6-from" v-model="form.ipv6.delegatedFrom" class="input" required>
              <option value="" disabled>Choose</option>
              <option v-for="u in upstreams" :key="u.name" :value="u.name">
                {{ u.name }} ({{ u.ipv6.prefixHint }})
              </option>
            </select>
          </FormField>
          <FormField
            id="if-v6-subnet"
            label="Subnet"
            hint="Which /64 of that prefix this interface takes. Each one needs its own."
          >
            <input
              id="if-v6-subnet"
              v-model.number="form.ipv6.subnetId"
              type="number"
              min="0"
              max="65535"
              class="input w-32 font-mono"
            />
          </FormField>
        </div>
        <div v-if="form.ipv6.mode === 'static'" class="grid gap-3 sm:grid-cols-2">
          <FormField id="if-v6-addr" label="Address (CIDR)">
            <input
              id="if-v6-addr"
              v-model="form.ipv6.address"
              class="input font-mono"
              placeholder="2001:db8::1/64"
              required
            />
          </FormField>
          <FormField id="if-v6-gw" label="Gateway">
            <input
              id="if-v6-gw"
              v-model="form.ipv6.gateway"
              class="input font-mono"
              placeholder="optional"
            />
          </FormField>
        </div>
      </fieldset>

      <FormField
        id="if-mtu"
        label="MTU"
        hint="0 leaves the MTU alone (the kernel default, or whatever was set before)."
      >
        <input
          id="if-mtu"
          v-model.number="form.mtu"
          type="number"
          min="0"
          max="65535"
          class="input w-32"
        />
      </FormField>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
