<script setup>
import { Plus, X } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { joinList, parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The site being edited, or null for a new one. */
  site: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return {
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
    waf: '',
  }
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
      allowFrom: joinList(s.allowFrom),
      waf: s.waf ?? '',
    }
  },
  { immediate: true },
)

const hosts = computed(() => parseList(form.value.hosts))

function addPath() {
  form.value.paths.push({ prefix: '', pool: pools.value[0]?.id ?? '' })
}

function removePath(index) {
  form.value.paths.splice(index, 1)
}

function save() {
  const f = form.value
  const site = {
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
  const allow = parseList(f.allowFrom)
  if (allow.length) site.allowFrom = allow
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
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
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

      <FormField id="site-hosts" label="Hostnames" hint="One per line. *.example.com is allowed.">
        <textarea
          id="site-hosts"
          v-model="form.hosts"
          class="input h-20 font-mono"
          required
          spellcheck="false"
        ></textarea>
      </FormField>

      <div class="grid gap-4 sm:grid-cols-2">
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

      <FormField id="site-allow" label="Allow from" hint="One prefix per line. Anyone when empty.">
        <textarea
          id="site-allow"
          v-model="form.allowFrom"
          class="input h-20 font-mono"
          spellcheck="false"
        ></textarea>
      </FormField>

      <fieldset class="space-y-2">
        <legend class="group-title">Paths to another pool</legend>
        <div v-for="(p, i) in form.paths" :key="i" class="flex items-center gap-2">
          <input
            v-model="p.prefix"
            class="input font-mono"
            placeholder="/api"
            spellcheck="false"
            :aria-label="`Path prefix ${i + 1}`"
          />
          <select
            v-model="p.pool"
            class="input w-40 font-mono max-sm:w-full"
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
        <label class="flex items-center gap-2 text-sm">
          <input v-model="form.plainHttp" type="checkbox" class="size-4 rounded border-line-2" />
          Serve on the HTTP port too
          <span class="text-ink-muted">Otherwise redirected.</span>
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-line-2" />
          Enabled
        </label>
      </div>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!hosts.length || !form.pool">
          Save to draft
        </button>
      </div>
    </form>
  </AppDialog>
</template>
