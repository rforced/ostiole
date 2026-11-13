<script setup lang="ts">
import { Plus, X } from '@lucide/vue'
import { computed, ref, watch } from 'vue'
import type { PropType } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { joinList, keptOrder, parseList } from '@/lib/lists'
import type { ProxyPath, ProxySite } from '@/lib/types.gen'
import { useConfigStore } from '@/stores/config'
import AllowFromAliases from '@/views/services/proxy/AllowFromAliases.vue'
import { splitAllowFrom } from '@/views/services/proxy/allowFrom'
import { redactPathsProblem } from '@/views/services/proxy/redactPaths'

const props = defineProps({
  /** The site being edited, or null for a new one. */
  site: { type: Object as PropType<ProxySite | null>, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  const form: {
    id: string
    previousId: string
    description: string
    enabled: boolean
    hosts: string
    certificate: string
    pool: string
    paths: ProxyPath[]
    plainHttp: boolean
    hostHeader: string
    allowFrom: string
    allowAliases: string[]
    redactPaths: string
    waf: string
  } = {
    id: '',
    previousId: '',
    description: '',
    enabled: true,
    hosts: '',
    certificate: '',
    pool: '',
    paths: [],
    plainHttp: false,
    hostHeader: '',
    allowFrom: '',
    allowAliases: [],
    redactPaths: '',
    waf: '',
  }
  return form
}

const pools = computed(() => config.proxy.pools ?? [])
const profiles = computed(() => config.proxy.wafProfiles ?? [])
const certificates = computed(() => config.certificates.filter((c) => c.enabled))

watch(
  () => [open.value, props.site],
  () => {
    if (!open.value) return
    const s = props.site
    if (!s) {
      form.value = blank()
      form.value.pool = pools.value[0]?.id ?? ''
      return
    }
    form.value = {
      ...blank(),
      id: s.id,
      previousId: s.id,
      description: s.description ?? '',
      enabled: s.enabled !== false,
      hosts: joinList(s.hosts),
      certificate: s.certificate ?? '',
      pool: s.pool ?? '',
      paths: (s.paths ?? []).map((p) => ({ ...p })),
      plainHttp: Boolean(s.plainHttp),
      hostHeader: s.hostHeader ?? '',
      allowFrom: joinList(splitAllowFrom(s.allowFrom, config.aliases).prefixes),
      allowAliases: splitAllowFrom(s.allowFrom, config.aliases).aliases,
      redactPaths: joinList(s.redactPaths),
      waf: s.waf ?? '',
    }
  },
  { immediate: true },
)

const hosts = computed(() => parseList(form.value.hosts))
const redactPaths = computed(() => parseList(form.value.redactPaths))

/** What the router would refuse. A list left as saved is never one, so an untouched save stays open. */
const redactProblem = computed(() => {
  const now = redactPaths.value
  if (props.site && keptOrder(props.site.redactPaths, now) !== now) return ''
  const others = (config.proxy.sites ?? [])
    .filter((s) => !form.value.previousId || s.id !== form.value.previousId)
    .flatMap((s) => s.redactPaths ?? [])
  return redactPathsProblem(now, others)
})

function addPath() {
  form.value.paths.push({ prefix: '', pool: pools.value[0]?.id ?? '' })
}

function removePath(index: number) {
  form.value.paths.splice(index, 1)
}

function save() {
  const f = form.value
  const site: ProxySite = {
    id: f.id.trim(),
    enabled: f.enabled,
    hosts: hosts.value,
    pool: f.pool,
  }
  if (f.description.trim()) site.description = f.description.trim()
  if (f.certificate) site.certificate = f.certificate
  const paths = f.paths.filter((p) => p.prefix.trim() && p.pool)
  if (paths.length) site.paths = paths.map((p) => ({ prefix: p.prefix.trim(), pool: p.pool }))
  if (f.plainHttp) site.plainHttp = true
  if (f.hostHeader) site.hostHeader = f.hostHeader
  const allow = keptOrder(props.site?.allowFrom, [...parseList(f.allowFrom), ...f.allowAliases])
  if (allow.length) site.allowFrom = allow
  const redact = keptOrder(props.site?.redactPaths, redactPaths.value)
  if (redact.length) site.redactPaths = redact
  if (f.waf) site.waf = f.waf
  config.upsertSite(site, f.previousId || site.id)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="site ? `Site ${site.id}` : 'Add site'"
    description="A set of hostnames served over HTTPS from a pool of backends."
  >
    <form id="site-form" class="space-y-4" @submit.prevent="save">
      <div class="fields">
        <FormField id="site-id" label="Name">
          <input
            id="site-id"
            v-model="form.id"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="site-desc" label="Description">
          <input id="site-desc" v-model="form.description" class="input" />
        </FormField>
      </div>

      <FormField
        id="site-hosts"
        label="Hostnames"
        hint="One per line. *.example.com is allowed. The rest resolve to this router where the proxy listens."
      >
        <textarea
          id="site-hosts"
          v-model="form.hosts"
          class="input h-20 font-mono"
          required
          spellcheck="false"
        ></textarea>
      </FormField>

      <div class="fields">
        <FormField
          id="site-cert"
          label="Certificate"
          hint="The built-in self-signed one by default."
        >
          <select id="site-cert" v-model="form.certificate" class="input">
            <option value="">Built-in self-signed</option>
            <option v-for="c in certificates" :key="c.id" :value="c.id">
              {{ c.description || c.id }}
            </option>
          </select>
        </FormField>
        <FormField id="site-pool" label="Pool">
          <select id="site-pool" v-model="form.pool" class="input font-mono" required>
            <option v-for="p in pools" :key="p.id" :value="p.id">{{ p.id }}</option>
          </select>
        </FormField>
        <FormField id="site-host-header" label="Host header" hint="The client's by default.">
          <select id="site-host-header" v-model="form.hostHeader" class="input">
            <option value="">Keep the client's</option>
            <option value="upstream">Send the upstream's address</option>
          </select>
        </FormField>
        <FormField id="site-waf" label="WAF profile" hint="Nothing is inspected when empty.">
          <select id="site-waf" v-model="form.waf" class="input font-mono">
            <option value="">None</option>
            <option v-for="w in profiles" :key="w.id" :value="w.id">{{ w.id }}</option>
          </select>
        </FormField>
      </div>

      <FormField
        id="site-allow"
        label="Allow from"
        hint="One prefix per line, and any alias ticked. Anyone when empty."
      >
        <textarea
          id="site-allow"
          v-model="form.allowFrom"
          class="input h-20 font-mono"
          spellcheck="false"
        ></textarea>
        <AllowFromAliases v-model="form.allowAliases" />
      </FormField>

      <FormField
        id="site-redact"
        label="Hidden path prefixes"
        hint="One per line, written like /s/. What follows a listed prefix is hidden in every log: the proxy's log line, the Requests tab and WAF events. The list works proxy-wide, so a prefix is hidden on every site. Nothing is hidden when empty."
      >
        <textarea
          id="site-redact"
          v-model="form.redactPaths"
          class="input h-20 font-mono"
          spellcheck="false"
        ></textarea>
        <ErrorLine v-if="redactProblem" class="text-sm">{{ redactProblem }}</ErrorLine>
      </FormField>

      <fieldset class="field-group">
        <legend>Paths to another pool</legend>
        <div v-for="(p, i) in form.paths" :key="i" class="flex items-center gap-2">
          <input
            v-model="p.prefix"
            class="input font-mono"
            spellcheck="false"
            :aria-label="`Path prefix ${i + 1}`"
          />
          <select
            v-model="p.pool"
            class="input w-48 font-mono max-sm:w-full"
            :aria-label="`Pool for path ${i + 1}`"
          >
            <option v-for="pool in pools" :key="pool.id" :value="pool.id">{{ pool.id }}</option>
          </select>
          <button
            type="button"
            class="icon-btn"
            :aria-label="`Remove path ${i + 1}`"
            @click="removePath(i)"
          >
            <X class="size-4" />
          </button>
        </div>
        <button type="button" class="btn-secondary" @click="addPath">
          <Plus class="size-4" aria-hidden="true" /> Add path
        </button>
      </fieldset>

      <div class="space-y-2">
        <ToggleRow
          v-model="form.plainHttp"
          label="Serve on the HTTP port too"
          hint="Otherwise redirected."
        />
        <ToggleRow v-model="form.enabled" label="Enabled" />
      </div>
    </form>
    <template #footer>
      <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
      <button
        type="submit"
        form="site-form"
        class="btn-primary"
        :disabled="!hosts.length || !form.pool || Boolean(redactProblem)"
      >
        Save to draft
      </button>
    </template>
  </AppDialog>
</template>
