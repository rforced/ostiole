<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import { parseList } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'

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

/** forward (dnsmasq asks upstreams), validate, or tls (both via unbound). */
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

const hostEditing = ref(null)
const hostOpen = ref(false)
const hostForm = ref({ hostname: '', ip: '', description: '' })
watch(
  () => [hostOpen.value, hostEditing.value],
  () => {
    if (hostOpen.value)
      hostForm.value = { hostname: '', ip: '', description: '', ...(hostEditing.value ?? {}) }
  },
)
function addHost() {
  hostEditing.value = null
  hostOpen.value = true
}
function editHost(h) {
  hostEditing.value = h
  hostOpen.value = true
}
function saveHost() {
  const out = { hostname: hostForm.value.hostname.trim(), ip: hostForm.value.ip.trim() }
  if (hostForm.value.description) out.description = hostForm.value.description
  config.upsertHostOverride(out, hostEditing.value?.hostname ?? out.hostname)
  hostOpen.value = false
}

const domainEditing = ref(null)
const domainOpen = ref(false)
const domainForm = ref({ domain: '', servers: '', description: '' })
watch(
  () => [domainOpen.value, domainEditing.value],
  () => {
    if (!domainOpen.value) return
    const d = domainEditing.value
    domainForm.value = {
      domain: d?.domain ?? '',
      servers: (d?.servers ?? []).join(', '),
      description: d?.description ?? '',
    }
  },
)
function addDomain() {
  domainEditing.value = null
  domainOpen.value = true
}
function editDomain(d) {
  domainEditing.value = d
  domainOpen.value = true
}
function saveDomain() {
  const out = {
    domain: domainForm.value.domain.trim(),
    servers: parseList(domainForm.value.servers),
  }
  if (domainForm.value.description) out.description = domainForm.value.description
  config.upsertDomainOverride(out, domainEditing.value?.domain ?? out.domain)
  domainOpen.value = false
}
</script>

<template>
  <div class="space-y-6">
    <label class="flex items-center gap-2 text-sm">
      <input v-model="dns.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
      <span class="font-medium">DNS service enabled</span>
      <span class="text-neutral-500">This router uses it too.</span>
    </label>

    <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
      <FormField id="dns-resolver" label="Resolver">
        <select id="dns-resolver" v-model="resolver" class="input">
          <option value="forward">Forward: ask the upstream resolvers below</option>
          <option value="validate">Validate: resolve from the root, check DNSSEC</option>
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

    <p v-if="resolver !== 'forward'" class="max-w-2xl text-sm text-neutral-500">
      Set up once with <code class="font-mono">ostiole services setup --with-resolver</code> as
      root.
    </p>

    <fieldset class="space-y-2 text-sm">
      <legend class="subsection-title">Listen on</legend>
      <label class="flex items-center gap-2">
        <input v-model="listenAll" type="checkbox" class="size-4 rounded border-neutral-300" />
        Every interface outside external zones
      </label>
      <div v-if="!listenAll" class="ml-6 flex flex-wrap gap-4">
        <label
          v-for="i in config.interfaces.filter((x) => x.zone && x.enabled)"
          :key="i.name"
          class="flex items-center gap-2"
        >
          <input
            type="checkbox"
            class="size-4 rounded border-neutral-300"
            :checked="(dns.interfaces ?? []).includes(i.name)"
            @change="toggleInterface(i.name, $event.target.checked)"
          />
          <span class="font-mono">{{ i.name }}</span>
        </label>
      </div>
    </fieldset>

    <section class="space-y-3" aria-labelledby="hosts-title">
      <div class="flex items-center gap-3">
        <h2 id="hosts-title" class="section-title">Host Overrides</h2>
        <button type="button" class="btn-secondary" @click="addHost">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add host
        </button>
      </div>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Hostname</th>
              <th>IP</th>
              <th>Description</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!(dns.hostOverrides ?? []).length" key="empty" class="row-static">
              <td colspan="4" class="text-neutral-500">
                No overrides. Static DHCP leases with hostnames resolve automatically.
              </td>
            </tr>
            <tr
              v-for="h in dns.hostOverrides"
              :key="h.hostname"
              :class="{ 'row-changed': config.isChanged('services.dns.hostOverrides', h.hostname) }"
            >
              <td class="font-mono text-code">{{ h.hostname }}</td>
              <td class="font-mono text-code">{{ h.ip }}</td>
              <td>{{ h.description }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editHost(h)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete the host override for ${h.hostname}?`"
                  :description="h.description"
                  @confirm="config.removeHostOverride(h.hostname)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </div>
    </section>

    <section class="space-y-3" aria-labelledby="domains-title">
      <div class="flex items-center gap-3">
        <h2 id="domains-title" class="section-title">Domain Overrides</h2>
        <button type="button" class="btn-secondary" @click="addDomain">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add domain
        </button>
      </div>
      <p class="max-w-2xl text-sm text-neutral-500">
        A domain here goes to its own resolvers, and stops answering while they are unreachable.
      </p>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Domain</th>
              <th>Resolvers</th>
              <th>Description</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!(dns.domainOverrides ?? []).length" key="empty" class="row-static">
              <td colspan="4" class="text-neutral-500">
                No overrides. Every domain goes to the resolver above.
              </td>
            </tr>
            <tr
              v-for="d in dns.domainOverrides"
              :key="d.domain"
              :class="{ 'row-changed': config.isChanged('services.dns.domainOverrides', d.domain) }"
            >
              <td class="font-mono text-code">{{ d.domain }}</td>
              <td class="font-mono text-code">{{ (d.servers ?? []).join(', ') }}</td>
              <td>{{ d.description }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editDomain(d)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete the override for ${d.domain}?`"
                  :description="d.description"
                  @confirm="config.removeDomainOverride(d.domain)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </div>
    </section>

    <AppDialog
      v-model:open="hostOpen"
      :title="hostEditing ? `Host ${hostEditing.hostname}` : 'New host override'"
    >
      <form class="space-y-4" @submit.prevent="saveHost">
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField id="ho-name" label="Hostname">
            <input
              id="ho-name"
              v-model="hostForm.hostname"
              class="input font-mono"
              required
              spellcheck="false"
            />
          </FormField>
          <FormField id="ho-ip" label="IP address">
            <input
              id="ho-ip"
              v-model="hostForm.ip"
              class="input font-mono"
              required
              spellcheck="false"
            />
          </FormField>
        </div>
        <FormField id="ho-desc" label="Description">
          <input id="ho-desc" v-model="hostForm.description" class="input" />
        </FormField>
        <div class="flex justify-end gap-2 pt-2">
          <button type="button" class="btn-secondary" @click="hostOpen = false">Cancel</button>
          <button type="submit" class="btn-primary">Save to draft</button>
        </div>
      </form>
    </AppDialog>

    <AppDialog
      v-model:open="domainOpen"
      :title="domainEditing ? `Domain ${domainEditing.domain}` : 'New domain override'"
    >
      <form class="space-y-4" @submit.prevent="saveDomain">
        <FormField id="do-domain" label="Domain" hint="Subdomains follow it, e.g. ts.net.">
          <input
            id="do-domain"
            v-model="domainForm.domain"
            class="input font-mono"
            required
            spellcheck="false"
            placeholder="ts.net"
          />
        </FormField>
        <FormField
          id="do-servers"
          label="Resolvers"
          hint="Comma separated. Add #port for anything but 53."
        >
          <input
            id="do-servers"
            v-model="domainForm.servers"
            class="input font-mono"
            required
            spellcheck="false"
            placeholder="100.100.100.100"
          />
        </FormField>
        <FormField id="do-desc" label="Description">
          <input id="do-desc" v-model="domainForm.description" class="input" />
        </FormField>
        <div class="flex justify-end gap-2 pt-2">
          <button type="button" class="btn-secondary" @click="domainOpen = false">Cancel</button>
          <button type="submit" class="btn-primary">Save to draft</button>
        </div>
      </form>
    </AppDialog>
  </div>
</template>
