<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The pool being edited, or null for a new one. */
  pool: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

const POLICIES = ['round_robin', 'least_conn', 'ip_hash', 'cookie', 'first']
const PROXY_PROTOCOLS = ['', 'v1', 'v2']

function blank() {
  return {
    id: '',
    previousId: '',
    description: '',
    upstreams: '',
    policy: '',
    healthPath: '',
    healthSeconds: 0,
    failSeconds: 0,
    tls: false,
    tlsServerName: '',
    tlsCaPem: '',
    tlsInsecure: false,
    proxyProtocol: '',
  }
}

watch(
  () => [open.value, props.pool],
  () => {
    if (!open.value) return
    const p = props.pool
    if (!p) {
      form.value = blank()
      return
    }
    form.value = {
      ...blank(),
      id: p.id,
      previousId: p.id,
      description: p.description ?? '',
      upstreams: joinList((p.upstreams ?? []).map((u) => u.address)),
      policy: p.policy ?? '',
      healthPath: p.healthPath ?? '',
      healthSeconds: p.healthSeconds ?? 0,
      failSeconds: p.failSeconds ?? 0,
      tls: Boolean(p.tls),
      tlsServerName: p.tlsServerName ?? '',
      tlsCaPem: p.tlsCaPem ?? '',
      tlsInsecure: Boolean(p.tlsInsecure),
      proxyProtocol: p.proxyProtocol ?? '',
    }
  },
  { immediate: true },
)

const upstreams = computed(() => parseList(form.value.upstreams))

function save() {
  const f = form.value
  const pool = {
    id: f.id.trim(),
    upstreams: upstreams.value.map((address) => ({ address })),
  }
  if (f.description.trim()) pool.description = f.description.trim()
  if (f.policy) pool.policy = f.policy
  if (f.healthPath.trim()) {
    pool.healthPath = f.healthPath.trim()
    if (Number(f.healthSeconds)) pool.healthSeconds = Number(f.healthSeconds)
  }
  if (Number(f.failSeconds)) pool.failSeconds = Number(f.failSeconds)
  if (f.tls) {
    pool.tls = true
    if (f.tlsServerName.trim()) pool.tlsServerName = f.tlsServerName.trim()
    if (f.tlsCaPem.trim()) pool.tlsCaPem = f.tlsCaPem.trim()
    if (f.tlsInsecure) pool.tlsInsecure = true
  }
  if (f.proxyProtocol) pool.proxyProtocol = f.proxyProtocol
  config.upsertPool(pool, f.previousId || pool.id)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="pool ? `Pool ${pool.id}` : 'Add pool'"
    description="Where a site's requests go, and how they are shared out."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="pool-id" label="Name">
          <input
            id="pool-id"
            v-model="form.id"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="pool-desc" label="Description">
          <input id="pool-desc" v-model="form.description" class="input" />
        </FormField>
      </div>

      <FormField id="pool-upstreams" label="Upstreams" hint="One host:port per line.">
        <textarea
          id="pool-upstreams"
          v-model="form.upstreams"
          class="input h-24 font-mono"
          required
          spellcheck="false"
        ></textarea>
      </FormField>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="pool-policy" label="Balancing">
          <select id="pool-policy" v-model="form.policy" class="input">
            <option value="">round_robin</option>
            <option v-for="p in POLICIES.slice(1)" :key="p" :value="p">{{ p }}</option>
          </select>
        </FormField>
        <FormField id="pool-proxy-protocol" label="PROXY protocol" hint="Off by default.">
          <select id="pool-proxy-protocol" v-model="form.proxyProtocol" class="input">
            <option v-for="p in PROXY_PROTOCOLS" :key="p" :value="p">{{ p || 'off' }}</option>
          </select>
        </FormField>
        <FormField id="pool-health" label="Health path" hint="Nothing is checked when empty.">
          <input
            id="pool-health"
            v-model="form.healthPath"
            class="input font-mono"
            placeholder="/healthz"
          />
        </FormField>
        <FormField id="pool-health-seconds" label="Health interval" hint="30.">
          <input
            id="pool-health-seconds"
            v-model.number="form.healthSeconds"
            type="number"
            min="0"
            max="3600"
            class="input w-32 font-mono"
            :disabled="!form.healthPath"
          />
        </FormField>
        <FormField
          id="pool-fail"
          label="Take out for"
          hint="0 keeps a failing upstream in. Seconds."
        >
          <input
            id="pool-fail"
            v-model.number="form.failSeconds"
            type="number"
            min="0"
            max="3600"
            class="input w-32 font-mono"
          />
        </FormField>
      </div>

      <fieldset class="space-y-3">
        <legend class="group-title">Upstream TLS</legend>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="form.tls" type="checkbox" class="size-4 rounded border-line-2" />
          HTTPS to the upstreams
        </label>
        <template v-if="form.tls">
          <FormField id="pool-sni" label="Server name" hint="The address when empty.">
            <input
              id="pool-sni"
              v-model="form.tlsServerName"
              class="input font-mono"
              spellcheck="false"
            />
          </FormField>
          <FormField id="pool-ca" label="Issuer certificate" hint="PEM. For a private issuer.">
            <textarea
              id="pool-ca"
              v-model="form.tlsCaPem"
              class="input h-24 font-mono text-code"
              spellcheck="false"
            ></textarea>
          </FormField>
          <label class="flex items-center gap-2 text-sm">
            <input
              v-model="form.tlsInsecure"
              type="checkbox"
              class="size-4 rounded border-line-2"
            />
            Do not check the upstream's certificate
            <span class="text-ink-muted">Trusts any certificate.</span>
          </label>
        </template>
      </fieldset>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!upstreams.length">
          Save to draft
        </button>
      </div>
    </form>
  </AppDialog>
</template>
