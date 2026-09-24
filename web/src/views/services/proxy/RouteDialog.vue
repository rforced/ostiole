<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The route being edited, or null for a new one. */
  route: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

const POLICIES = ['round_robin', 'least_conn', 'ip_hash', 'first', 'random']
const PROXY_PROTOCOLS = ['', 'v1', 'v2']

function blank() {
  return {
    id: '',
    previousId: '',
    description: '',
    enabled: true,
    protocol: 'tcp',
    port: 0,
    sni: '',
    upstreams: '',
    policy: '',
    healthSeconds: 0,
    proxyProtocol: '',
    allowFrom: '',
  }
}

watch(
  () => [open.value, props.route],
  () => {
    if (!open.value) return
    const r = props.route
    if (!r) {
      form.value = blank()
      return
    }
    form.value = {
      ...blank(),
      id: r.id,
      previousId: r.id,
      description: r.description ?? '',
      enabled: r.enabled !== false,
      protocol: r.protocol ?? 'tcp',
      port: r.port ?? 0,
      sni: joinList(r.sni),
      upstreams: joinList((r.upstreams ?? []).map((u) => u.address)),
      policy: r.policy ?? '',
      healthSeconds: r.healthSeconds ?? 0,
      proxyProtocol: r.proxyProtocol ?? '',
      allowFrom: joinList(r.allowFrom),
    }
  },
  { immediate: true },
)

const isTCP = computed(() => form.value.protocol === 'tcp')
const upstreams = computed(() => parseList(form.value.upstreams))

function save() {
  const f = form.value
  const route = {
    id: f.id.trim(),
    enabled: f.enabled,
    protocol: f.protocol,
    port: Number(f.port),
    upstreams: upstreams.value.map((address) => ({ address })),
  }
  if (f.description.trim()) route.description = f.description.trim()
  if (isTCP.value) {
    const sni = parseList(f.sni)
    if (sni.length) route.sni = sni
    if (Number(f.healthSeconds)) route.healthSeconds = Number(f.healthSeconds)
  }
  if (f.policy) route.policy = f.policy
  if (f.proxyProtocol) route.proxyProtocol = f.proxyProtocol
  const allow = parseList(f.allowFrom)
  if (allow.length) route.allowFrom = allow
  config.upsertProxyRoute(route, f.previousId || route.id)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="route ? `Route ${route.id}` : 'Add route'"
    description="One port passed through, whole or by the name a TLS client asks for."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="route-id" label="Name">
          <input
            id="route-id"
            v-model="form.id"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="route-desc" label="Description">
          <input id="route-desc" v-model="form.description" class="input" />
        </FormField>
        <FormField id="route-protocol" label="Protocol">
          <select id="route-protocol" v-model="form.protocol" class="input w-32 max-sm:w-full">
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
          </select>
        </FormField>
        <FormField id="route-port" label="Port">
          <input
            id="route-port"
            v-model.number="form.port"
            type="number"
            min="1"
            max="65535"
            class="input w-32 font-mono max-sm:w-full"
            required
          />
        </FormField>
      </div>

      <FormField
        id="route-sni"
        label="Server names"
        hint="One per line. The whole port when empty. TCP only."
      >
        <textarea
          id="route-sni"
          v-model="form.sni"
          class="input h-20 font-mono"
          :disabled="!isTCP"
          spellcheck="false"
        ></textarea>
      </FormField>

      <FormField id="route-upstreams" label="Upstreams" hint="One host:port per line.">
        <textarea
          id="route-upstreams"
          v-model="form.upstreams"
          class="input h-20 font-mono"
          required
          spellcheck="false"
        ></textarea>
      </FormField>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="route-policy" label="Balancing">
          <select id="route-policy" v-model="form.policy" class="input">
            <option value="">round_robin</option>
            <option v-for="p in POLICIES.slice(1)" :key="p" :value="p">{{ p }}</option>
          </select>
        </FormField>
        <FormField id="route-health" label="Health interval" hint="0 never. Seconds. TCP only.">
          <input
            id="route-health"
            v-model.number="form.healthSeconds"
            type="number"
            min="0"
            max="3600"
            class="input w-32 font-mono max-sm:w-full"
            :disabled="!isTCP"
          />
        </FormField>
        <FormField id="route-proxy-protocol" label="PROXY protocol" hint="Off by default.">
          <select id="route-proxy-protocol" v-model="form.proxyProtocol" class="input">
            <option v-for="p in PROXY_PROTOCOLS" :key="p" :value="p">{{ p || 'off' }}</option>
          </select>
        </FormField>
      </div>

      <FormField id="route-allow" label="Allow from" hint="One prefix per line. Anyone when empty.">
        <textarea
          id="route-allow"
          v-model="form.allowFrom"
          class="input h-20 font-mono"
          spellcheck="false"
        ></textarea>
      </FormField>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-line-2" />
        Enabled
      </label>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!upstreams.length || !form.port">
          Save to draft
        </button>
      </div>
    </form>
  </AppDialog>
</template>
