<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { newId } from '@/lib/ids'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'
import EndpointFields from '@/views/firewall/EndpointFields.vue'

const props = defineProps({
  /** The access rule being edited, or null for a new one. */
  line: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

const routes = computed(() => config.proxy.routes ?? [])
const httpPort = computed(() => config.proxy.httpPort || 80)
const httpsPort = computed(() => config.proxy.httpsPort || 443)

function blank() {
  return {
    id: '',
    enabled: true,
    zone: config.zones.find((z) => z.external)?.name ?? config.zones[0]?.name ?? '',
    ports: ['https'],
    routes: [],
    source: { mode: 'any', addresses: '', alias: '', peer: '', notAddresses: false },
    action: 'accept',
    log: false,
    description: '',
  }
}

watch(
  () => [open.value, props.line],
  () => {
    if (!open.value) return
    const a = props.line
    if (!a) {
      form.value = blank()
      return
    }
    const src = a.source ?? {}
    form.value = {
      ...blank(),
      id: a.id,
      enabled: a.enabled !== false,
      zone: a.zone,
      ports: [...(a.ports ?? [])],
      routes: [...(a.routes ?? [])],
      source: {
        mode: src.alias ? 'alias' : src.peer ? 'peer' : src.addresses?.length ? 'addresses' : 'any',
        addresses: joinList(src.addresses),
        alias: src.alias ?? '',
        peer: src.peer ?? '',
        notAddresses: Boolean(src.notAddresses),
      },
      action: a.action ?? 'accept',
      log: Boolean(a.log),
      description: a.description ?? '',
    }
  },
  { immediate: true },
)

const named = computed(() => form.value.ports.length + form.value.routes.length > 0)

/** HTTP alone is mostly a redirect: a site answers it by sending the client to HTTPS. */
const redirectOnly = computed(
  () => form.value.ports.includes('http') && !form.value.ports.includes('https'),
)

function save() {
  const f = form.value
  const source = {}
  if (f.source.mode === 'addresses') source.addresses = parseList(f.source.addresses)
  if (f.source.mode === 'alias') source.alias = f.source.alias
  if (f.source.mode === 'peer') source.peer = f.source.peer
  if (f.source.notAddresses && f.source.mode !== 'any') source.notAddresses = true
  const out = {
    id: f.id || newId('access'),
    enabled: f.enabled,
    zone: f.zone,
    source,
    action: f.action,
  }
  // In the order the page lists them, whatever order they were ticked in.
  const ports = ['http', 'https'].filter((p) => f.ports.includes(p))
  if (ports.length) out.ports = ports
  const chosen = routes.value.map((r) => r.id).filter((id) => f.routes.includes(id))
  if (chosen.length) out.routes = chosen
  if (f.log) out.log = true
  if (f.description.trim()) out.description = f.description.trim()
  config.upsertProxyAccess(out)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="line ? 'Access rule' : 'Add access rule'"
    description="The first rule in a zone that matches a connection decides it."
  >
    <form id="proxy-access-form" class="space-y-4" @submit.prevent="save">
      <FormField id="access-desc" label="Description">
        <input id="access-desc" v-model="form.description" class="input" />
      </FormField>
      <div class="fields">
        <FormField id="access-zone" label="Zone">
          <select id="access-zone" v-model="form.zone" class="input font-mono" required>
            <option v-for="z in config.zones" :key="z.name" :value="z.name">{{ z.name }}</option>
          </select>
        </FormField>
        <FormField id="access-action" label="Action">
          <select id="access-action" v-model="form.action" class="input">
            <option value="accept">Accept</option>
            <option value="drop">Drop</option>
            <option value="reject">Reject</option>
          </select>
        </FormField>
      </div>

      <fieldset class="field-group">
        <legend>Ports</legend>
        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-2">
            <input v-model="form.ports" type="checkbox" value="http" class="checkbox" />
            HTTP <span class="font-mono text-ink-muted">{{ httpPort }}</span>
          </label>
          <label class="flex items-center gap-2">
            <input v-model="form.ports" type="checkbox" value="https" class="checkbox" />
            HTTPS <span class="font-mono text-ink-muted">{{ httpsPort }}</span>
          </label>
          <label v-for="r in routes" :key="r.id" class="flex items-center gap-2">
            <input v-model="form.routes" type="checkbox" :value="r.id" class="checkbox" />
            <span class="font-mono">{{ r.id }}</span>
            <span class="font-mono text-ink-muted">{{ r.protocol }}/{{ r.port }}</span>
          </label>
        </div>
        <p v-if="redirectOnly" class="text-sm text-ink-muted">
          Sites send HTTP to HTTPS unless they serve plain HTTP.
        </p>
      </fieldset>

      <EndpointFields v-model="form.source" side="source" :zone="form.zone" />

      <div class="flex flex-wrap gap-4 text-sm">
        <ToggleRow v-model="form.enabled" label="Enabled" />
        <ToggleRow
          v-model="form.log"
          label="Log matches"
          :hint="form.action === 'accept' ? '' : 'Logs at most 10 packets a second.'"
        />
      </div>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button
        type="submit"
        form="proxy-access-form"
        class="btn-primary"
        :disabled="!named || !form.zone"
      >
        Save to draft
      </button>
    </template>
  </AppDialog>
</template>
