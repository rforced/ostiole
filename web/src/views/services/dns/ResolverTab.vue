<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import AppDisclosure from '@/components/AppDisclosure.vue'
import FormField from '@/components/FormField.vue'
import InterfaceLabel from '@/components/InterfaceLabel.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { dnsListenInterfaces } from '@/lib/interfaces'
import { parseList } from '@/lib/lists'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()
const dns = computed(() => config.ensureServices().dns)

const upstreams = computed({
  get: () => (dns.value.upstreams ?? []).join(', '),
  set: (v) => {
    const list = parseList(v)
    if (list.length) dns.value.upstreams = list
    else delete dns.value.upstreams
  },
})
const domain = computed({
  get: () => dns.value.domain ?? '',
  set: (v) => {
    if (v.trim()) dns.value.domain = v.trim()
    else delete dns.value.domain
  },
})

/** forward (dnsmasq asks upstreams), recursive, or tls (both via unbound). */
const resolver = computed({
  get: () => dns.value.resolver ?? 'forward',
  set: (v) => {
    if (v === 'forward') delete dns.value.resolver
    else dns.value.resolver = v
    if (v === 'tls' && !(dns.value.tlsUpstreams ?? []).length) {
      dns.value.tlsUpstreams = [
        { address: '1.1.1.1', hostname: 'cloudflare-dns.com' },
        { address: '9.9.9.9', hostname: 'dns.quad9.net' },
      ]
    }
  },
})

/** Who can read the names looked up, which is what the choice comes down to. */
const resolverHints = {
  forward: 'Unencrypted. The upstream resolvers and your ISP see every lookup.',
  recursive: 'Unencrypted. No single server sees every lookup, but your ISP can.',
  tls: 'Encrypted. Only the servers below see every lookup.',
}

/** One "address hostname" pair per line, which is how DoT servers are quoted. */
const tlsUpstreams = computed({
  get: () => (dns.value.tlsUpstreams ?? []).map((u) => `${u.address} ${u.hostname}`).join('\n'),
  set: (v) => {
    const list = v
      .split('\n')
      .map((line) => line.trim().split(/[\s,]+/))
      .filter((parts) => parts[0])
      .map(([address, hostname = '']) => ({ address, hostname }))
    if (list.length) dns.value.tlsUpstreams = list
    else delete dns.value.tlsUpstreams
  },
})

/** Empty means the default, which the placeholder shows. */
function numberField(key) {
  return computed({
    get: () => dns.value[key] || '',
    set: (v) => {
      if (Number.isFinite(v) && v > 0) dns.value[key] = v
      else delete dns.value[key]
    },
  })
}
const cacheSize = numberField('cacheSize')
const resolverCacheMB = numberField('resolverCacheMB')

/** Writes the rebind block back, or drops it once nothing is left in it. */
function setRebind(r) {
  if (Object.keys(r).length) dns.value.rebind = r
  else delete dns.value.rebind
}
/** Rebinding protection is on unless the configuration says off. */
const rebindOn = computed({
  get: () => !dns.value.rebind?.off,
  set: (on) => {
    const r = { ...(dns.value.rebind ?? {}) }
    if (on) delete r.off
    else r.off = true
    setRebind(r)
  },
})
const rebindAllow = computed({
  get: () => (dns.value.rebind?.allow ?? []).join(', '),
  set: (v) => {
    const r = { ...(dns.value.rebind ?? {}) }
    const list = parseList(v)
    if (list.length) r.allow = list
    else delete r.allow
    setRebind(r)
  },
})

// Clearing acts on the running router, not the draft, so it reads the
// saved configuration and never touches the one being edited.
const dnsRunning = computed(() => Boolean(config.saved?.services?.dns?.enabled))
const cleared = ref('')
const names = { dnsmasq: 'the DNS server', unbound: 'the validating resolver' }
const clearCache = useAsync(async () => {
  cleared.value = ''
  const { cleared: what = [] } = await api.clearDnsCache()
  cleared.value = what.length
    ? `Cleared ${what.map((n) => names[n] ?? n).join(' and ')}.`
    : 'Nothing was running to clear.'
})

/** What "every interface outside external zones" comes to for this draft. */
const defaultListen = computed(() => dnsListenInterfaces(config.draft))

const listenAll = computed({
  get: () => !(dns.value.interfaces ?? []).length,
  set: (all) => {
    if (all) delete dns.value.interfaces
    else
      dns.value.interfaces = config.interfaces.filter((i) => i.zone && i.enabled).map((i) => i.name)
  },
})

function toggleInterface(name, on) {
  const list = new Set(dns.value.interfaces ?? [])
  if (on) list.add(name)
  else list.delete(name)
  dns.value.interfaces = [...list]
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Resolver"
      intro="This router looks names up here too."
      :locked="auth.readOnly"
    >
      <div class="space-y-4">
        <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
          <FormField id="dns-resolver" label="Resolver" :hint="resolverHints[resolver]">
            <select id="dns-resolver" v-model="resolver" class="input">
              <option value="forward">Forward: ask the upstream resolvers below</option>
              <option value="recursive">Recursive: look names up directly, check DNSSEC</option>
              <option value="tls">DNS over TLS: encrypted upstreams, check DNSSEC</option>
            </select>
          </FormField>
          <FormField id="dns-domain" label="Local domain" hint="Hosts get this suffix, e.g. lan.">
            <input id="dns-domain" v-model="domain" class="input font-mono" spellcheck="false" />
          </FormField>
          <FormField
            v-if="resolver === 'forward'"
            id="dns-up"
            label="Upstream resolvers"
            hint="Comma separated. Empty: the system resolvers."
          >
            <input
              id="dns-up"
              v-model="upstreams"
              class="input font-mono"
              spellcheck="false"
              placeholder="1.1.1.1, 9.9.9.9"
            />
          </FormField>
          <FormField
            v-if="resolver === 'tls'"
            id="dns-tls"
            label="DNS over TLS servers"
            hint="One per line: address and the name on its certificate."
            class="sm:col-span-2"
          >
            <textarea
              id="dns-tls"
              v-model="tlsUpstreams"
              rows="3"
              class="input font-mono"
              spellcheck="false"
              placeholder="1.1.1.1 cloudflare-dns.com"
            ></textarea>
          </FormField>
        </div>

        <AppDisclosure>
          <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
            <FormField
              id="dns-cache"
              label="Cache entries"
              hint="10000 is the default. Each is about 100 bytes."
            >
              <input
                id="dns-cache"
                v-model.number="cacheSize"
                type="number"
                min="0"
                max="1000000"
                placeholder="10000"
                class="input w-32 max-sm:w-full"
              />
            </FormField>
            <FormField
              v-if="resolver !== 'forward'"
              id="dns-resolver-cache"
              label="Resolver cache (MB)"
              hint="10 is the default. Records take twice as much again."
            >
              <input
                id="dns-resolver-cache"
                v-model.number="resolverCacheMB"
                type="number"
                min="0"
                max="512"
                placeholder="10"
                class="input w-32 max-sm:w-full"
              />
            </FormField>
          </div>
          <div v-if="!auth.readOnly" class="flex flex-wrap items-center gap-3">
            <button
              type="button"
              class="btn-secondary"
              :disabled="!dnsRunning || clearCache.busy.value"
              :aria-busy="clearCache.busy.value"
              @click="clearCache.run()"
            >
              <LoaderCircle
                v-if="clearCache.busy.value"
                class="size-4 animate-spin"
                aria-hidden="true"
              />
              {{ clearCache.busy.value ? 'Clearing…' : 'Clear cache' }}
            </button>
            <p v-if="clearCache.error.value" role="alert" class="text-bad">
              {{ clearCache.error.value }}
            </p>
            <p v-else-if="cleared" role="status" class="text-ink-muted">{{ cleared }}</p>
            <p v-else class="text-ink-muted">Names are looked up again on the running resolver.</p>
          </div>

          <fieldset class="space-y-2">
            <legend class="group-title mb-2">Listen on</legend>
            <ToggleRow v-model="listenAll" label="Every interface outside external zones" />
            <ul v-if="listenAll" class="ml-6 flex flex-wrap gap-4" aria-label="Listening on">
              <li v-if="!defaultListen.length" class="text-ink-muted">
                No enabled interface is in an internal zone yet.
              </li>
              <li v-for="i in defaultListen" :key="i.name"><InterfaceLabel :iface="i" /></li>
            </ul>
            <div v-else class="ml-6 flex flex-wrap gap-4">
              <label
                v-for="i in config.interfaces.filter((x) => x.zone && x.enabled)"
                :key="i.name"
                class="flex items-center gap-2"
              >
                <input
                  type="checkbox"
                  class="size-4 rounded"
                  :checked="(dns.interfaces ?? []).includes(i.name)"
                  @change="toggleInterface(i.name, $event.target.checked)"
                />
                <InterfaceLabel :iface="i" />
              </label>
            </div>
          </fieldset>
        </AppDisclosure>
      </div>
    </SectionCard>

    <SectionCard title="Answers" :locked="auth.readOnly">
      <div class="space-y-4">
        <ToggleRow
          id="dns-rebind"
          v-model="rebindOn"
          label="Block private answers from upstream"
          hint="Off lets a public name resolve to a LAN address."
        />
        <FormField
          v-if="rebindOn"
          id="dns-rebind-allow"
          label="Allowed domains"
          hint="Comma separated. The local domain, overrides and the tailnet are always allowed."
        >
          <input
            id="dns-rebind-allow"
            v-model="rebindAllow"
            class="input max-w-2xl font-mono"
            spellcheck="false"
            placeholder="home.example.com"
          />
        </FormField>
      </div>
    </SectionCard>
  </div>
</template>
