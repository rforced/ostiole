<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { PropType } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { interfaceLabel } from '@/lib/interfaces'
import { parseList } from '@/lib/lists'
import type { DHCPServer } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  server: { type: Object as PropType<DHCPServer | null>, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

/** Interfaces that can serve DHCP: static IPv4 and without a server yet (unless editing). */
const candidates = computed(() =>
  config.interfaces.filter(
    (i) =>
      i.ipv4?.mode === 'static' &&
      (props.server?.interface === i.name ||
        !(config.draft.services?.dhcp?.servers ?? []).some((s) => s.interface === i.name)),
  ),
)

function blank() {
  return {
    interface: '',
    enabled: true,
    rangeStart: '',
    rangeEnd: '',
    leaseTime: '',
    gateway: '',
    dns: '',
    domain: '',
    searchDomains: '',
    ntp: '',
    bootServer: '',
    bootFile: '',
    dnsRegistration: false,
  }
}

watch(
  () => [open.value, props.server],
  () => {
    if (!open.value) return
    const s = props.server
    form.value = s
      ? {
          ...blank(),
          ...s,
          dns: (s.dns ?? []).join(', '),
          searchDomains: (s.searchDomains ?? []).join(', '),
          ntp: (s.ntp ?? []).join(', '),
        }
      : blank()
    if (!s && candidates.value.length === 1) form.value.interface = candidates.value[0].name
  },
  { immediate: true },
)

/** Where a device's own name resolves once it is registered. */
const registration = computed(() => {
  const domain = config.draft?.services?.dns?.domain
  return domain ? `Names devices send resolve under ${domain}.` : 'Names devices send resolve.'
})

/** Suggest hosts 100-199 of the interface's /24 when the fields are empty. */
watch(
  () => form.value.interface,
  (name) => {
    const iface = config.findInterface(name)
    if (!iface?.ipv4?.address || form.value.rangeStart) return
    const [addr, bits] = iface.ipv4.address.split('/')
    if (Number(bits) > 24) return
    const octets = addr.split('.')
    if (octets.length !== 4) return
    form.value.rangeStart = `${octets[0]}.${octets[1]}.${octets[2]}.100`
    form.value.rangeEnd = `${octets[0]}.${octets[1]}.${octets[2]}.199`
  },
)

function save() {
  const f = form.value
  const out: DHCPServer = {
    interface: f.interface,
    enabled: f.enabled,
    rangeStart: f.rangeStart.trim(),
    rangeEnd: f.rangeEnd.trim(),
  }
  if (f.leaseTime) out.leaseTime = f.leaseTime.trim()
  if (f.gateway) out.gateway = f.gateway.trim()
  const dns = parseList(f.dns)
  if (dns.length) out.dns = dns
  if (f.domain) out.domain = f.domain.trim()
  const searchDomains = parseList(f.searchDomains)
  if (searchDomains.length) out.searchDomains = searchDomains
  const ntp = parseList(f.ntp)
  if (ntp.length) out.ntp = ntp
  if (f.bootServer) out.bootServer = f.bootServer.trim()
  if (f.bootFile) out.bootFile = f.bootFile.trim()
  if (f.dnsRegistration) out.dnsRegistration = true
  config.upsertServer(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="server ? `DHCP on ${server.interface}` : 'Add DHCP server'"
    description="One pool per interface. The interface needs a static IPv4 address."
  >
    <form id="dhcp-server-form" class="space-y-4" @submit.prevent="save">
      <FormField id="sc-if" label="Interface">
        <select id="sc-if" v-model="form.interface" class="input" required :disabled="!!server">
          <option value="" disabled>Choose</option>
          <option v-for="i in candidates" :key="i.name" :value="i.name">
            {{ interfaceLabel(i) }} ({{ i.ipv4.address }})
          </option>
        </select>
      </FormField>
      <div class="fields">
        <FormField id="sc-start" label="Range start">
          <input
            id="sc-start"
            v-model="form.rangeStart"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="sc-end" label="Range end">
          <input
            id="sc-end"
            v-model="form.rangeEnd"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField
          id="sc-lease"
          label="Lease time"
          hint="24h is the default. 2d works too, and infinite never expires."
        >
          <input
            id="sc-lease"
            v-model="form.leaseTime"
            placeholder="24h"
            class="input w-32 font-mono max-sm:w-full"
            spellcheck="false"
          />
        </FormField>
        <FormField id="sc-gw" label="Gateway" hint="Empty: this router.">
          <input id="sc-gw" v-model="form.gateway" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField
          id="sc-dns"
          label="DNS servers"
          hint="Empty: this router when the DNS service is on, else the system resolvers."
        >
          <input id="sc-dns" v-model="form.dns" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField id="sc-domain" label="Domain" hint="Empty: the DNS service's domain.">
          <input id="sc-domain" v-model="form.domain" class="input font-mono" spellcheck="false" />
        </FormField>
        <FormField
          id="sc-search"
          label="Search domains"
          hint="Comma separated. Empty: the domain."
          class="sm:col-span-2"
        >
          <input
            id="sc-search"
            v-model="form.searchDomains"
            class="input font-mono"
            spellcheck="false"
          />
        </FormField>
        <FormField
          id="sc-time"
          label="Time servers"
          hint="Empty: this router when it answers time requests."
        >
          <input id="sc-time" v-model="form.ntp" class="input font-mono" spellcheck="false" />
        </FormField>
      </div>
      <fieldset class="field-group">
        <legend>Network boot</legend>
        <div class="fields">
          <FormField
            id="sc-boot-server"
            label="Boot server"
            hint="The TFTP server's IPv4 address. Empty: no network boot."
          >
            <input
              id="sc-boot-server"
              v-model="form.bootServer"
              class="input w-48 font-mono max-sm:w-full"
              spellcheck="false"
            />
          </FormField>
          <FormField
            id="sc-boot-file"
            label="Boot file"
            hint="The file to load, like pxelinux.0. Needs a boot server."
          >
            <input
              id="sc-boot-file"
              v-model="form.bootFile"
              class="input font-mono"
              spellcheck="false"
            />
          </FormField>
        </div>
      </fieldset>
      <div class="space-y-2">
        <ToggleRow v-model="form.dnsRegistration" label="DNS registration" :hint="registration" />
        <ToggleRow v-model="form.enabled" label="Enabled" />
      </div>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button type="submit" form="dhcp-server-form" class="btn-primary" :disabled="!form.interface">
        Save to draft
      </button>
    </template>
  </AppDialog>
</template>
