<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { overrideKey, overrideName } from '@/lib/hosts'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The override being edited, or null for a new one. */
  override: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const dns = computed(() => config.ensureServices().dns)

const form = ref({ hostname: '', domain: '', ip: '', aliases: '', description: '' })
/** The DHCP leases, read each time the dialog opens. */
const leases = ref([])
watch(open, async (on) => {
  if (!on) return
  const h = props.override
  form.value = {
    hostname: h?.hostname ?? '',
    domain: h?.domain ?? '',
    ip: h?.ip ?? '',
    aliases: (h?.aliases ?? []).join(', '),
    description: h?.description ?? '',
  }
  leases.value = []
  try {
    leases.value = await api.services.leases()
  } catch {
    // Without them the dialog only loses the warning below.
  }
})

const domainOf = (s) =>
  (s ?? '')
    .trim()
    .replace(/^\.+|\.+$/g, '')
    .toLowerCase()

/**
 * Devices that asked the DHCP server for one of the names typed, at
 * another address of the same family. The router answers the override,
 * so a device keeps its address and loses the name. A static lease that
 * names its device is left to the check on apply, which refuses the pair.
 */
const taken = computed(() => {
  const local = domainOf(dns.value.domain)
  const domain = domainOf(form.value.domain)
  if (domain && domain !== local) return []
  const names = new Set()
  for (const label of [form.value.hostname, ...parseList(form.value.aliases)]) {
    const name = label.trim().toLowerCase()
    if (!name) continue
    names.add(name)
    if (local) names.add(`${name}.${local}`)
  }
  const named = new Set(
    (config.draft?.services?.dhcp?.staticLeases ?? [])
      .filter((s) => s.hostname)
      .map((s) => s.mac.toLowerCase()),
  )
  const ip = form.value.ip.trim()
  return leases.value.filter(
    (l) =>
      l.hostname &&
      names.has(l.hostname.toLowerCase()) &&
      !named.has(l.mac?.toLowerCase()) &&
      l.ip !== ip &&
      l.ip.includes(':') === ip.includes(':'),
  )
})

const title = computed(() =>
  props.override ? `Host ${overrideName(props.override, dns.value.domain)}` : 'Add host override',
)

function save() {
  const out = { hostname: form.value.hostname.trim(), ip: form.value.ip.trim() }
  const domain = form.value.domain.trim()
  if (domain) out.domain = domain
  const aliases = parseList(form.value.aliases)
  if (aliases.length) out.aliases = aliases
  if (form.value.description) out.description = form.value.description
  config.upsertHostOverride(out, overrideKey(props.override ?? out))
  open.value = false
}
</script>

<template>
  <AppDialog v-model:open="open" :title="title">
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="ho-name" label="Hostname">
          <input
            id="ho-name"
            v-model="form.hostname"
            class="input font-mono"
            required
            spellcheck="false"
            :aria-describedby="taken.length ? 'ho-taken' : undefined"
          />
        </FormField>
        <FormField
          id="ho-domain"
          label="Domain"
          :hint="
            dns.domain
              ? `Empty means ${dns.domain}. The name answers bare too.`
              : 'Empty means no domain.'
          "
        >
          <input
            id="ho-domain"
            v-model="form.domain"
            class="input font-mono"
            spellcheck="false"
            :placeholder="dns.domain"
          />
        </FormField>
      </div>
      <div v-if="taken.length" id="ho-taken" class="space-y-1 text-sm text-warn">
        <p v-for="l in taken" :key="l.ip">
          A device at {{ l.ip }} asks for {{ l.hostname }} as well. It keeps its address and loses
          the name.
        </p>
      </div>
      <FormField id="ho-ip" label="Address">
        <input id="ho-ip" v-model="form.ip" class="input font-mono" required spellcheck="false" />
      </FormField>
      <FormField id="ho-aliases" label="Aliases" hint="Comma separated. Same domain, same address.">
        <input id="ho-aliases" v-model="form.aliases" class="input font-mono" spellcheck="false" />
      </FormField>
      <FormField id="ho-desc" label="Description">
        <input id="ho-desc" v-model="form.description" class="input" />
      </FormField>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
