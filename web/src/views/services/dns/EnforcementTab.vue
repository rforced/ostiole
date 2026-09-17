<script setup>
import { computed } from 'vue'

import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const blocking = computed(() => config.ensureBlocking())
const enforce = computed(() => config.ensureBlocking().enforce)

/** Aliases that hold addresses; port aliases are no use here. */
const addressAliases = computed(() =>
  config.aliases.filter((a) => a.type === 'hosts' || a.type === 'geoip'),
)

/** A published list of the addresses public DoH resolvers answer on. */
const DOH_LIST_URL =
  'https://raw.githubusercontent.com/dibdot/DoH-IP-blocklists/master/doh-ipv4.txt'

/** Empty string in a select means "none", which the model wants absent. */
function aliasField(key) {
  return computed({
    get: () => enforce.value[key] ?? '',
    set: (v) => {
      if (v) enforce.value[key] = v
      else delete enforce.value[key]
    },
  })
}

const dohAlias = aliasField('dohAlias')
const exemptAlias = aliasField('exemptAlias')
</script>

<template>
  <div class="max-w-3xl space-y-6">
    <p class="text-sm text-neutral-500">
      Blocking a name achieves nothing if the client asks someone else instead. These keep clients
      on this box's resolver. They render firewall rules, so they show up in the ruleset like
      everything else.
    </p>

    <p
      v-if="!blocking.enabled"
      role="note"
      class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-100"
    >
      DNS blocking is off, so none of this is rendered. Turn it on under Block lists.
    </p>

    <fieldset class="space-y-3 text-sm">
      <legend class="font-medium">Keep clients here</legend>

      <label class="flex items-start gap-2">
        <input
          v-model="enforce.redirectDns"
          type="checkbox"
          class="mt-0.5 size-4 rounded border-neutral-300"
        />
        <span>
          <span class="font-medium">Send all plain DNS to this box</span>
          <span class="block text-neutral-500">
            A client that insists on 8.8.8.8 still gets answers, just this box's answers. Queries
            already addressed here are left alone.
          </span>
        </span>
      </label>

      <label class="flex items-start gap-2">
        <input
          v-model="enforce.blockDot"
          type="checkbox"
          class="mt-0.5 size-4 rounded border-neutral-300"
        />
        <span>
          <span class="font-medium">Drop DNS over TLS</span>
          <span class="block text-neutral-500">
            Port 853 is the easy half of stopping encrypted DNS: it has a port of its own.
          </span>
        </span>
      </label>

      <label class="flex items-start gap-2">
        <input
          v-model="enforce.firefoxCanary"
          type="checkbox"
          class="mt-0.5 size-4 rounded border-neutral-300"
        />
        <span>
          <span class="font-medium">Ask Firefox not to turn on DNS over HTTPS</span>
          <span class="block text-neutral-500">
            Answers <span class="font-mono">use-application-dns.net</span> with NXDOMAIN, which is
            how Firefox is told this network would rather it did not.
          </span>
        </span>
      </label>
    </fieldset>

    <FormField
      id="enf-doh"
      label="Drop traffic to DNS over HTTPS servers"
      hint="DoH hides on port 443, so only an address list catches it. Make a fetched host alias under Firewall → Aliases and name it here."
    >
      <select id="enf-doh" v-model="dohAlias" class="input">
        <option value="">Not blocked</option>
        <option v-for="a in addressAliases" :key="a.name" :value="a.name">{{ a.name }}</option>
      </select>
    </FormField>

    <p class="text-sm text-neutral-500">
      A published list of those addresses:
      <span class="font-mono break-all">{{ DOH_LIST_URL }}</span>
    </p>

    <FormField
      id="enf-exempt"
      label="Except these clients"
      hint="Addresses in this alias are left alone by everything above: the one machine allowed to resolve for itself."
    >
      <select id="enf-exempt" v-model="exemptAlias" class="input">
        <option value="">No exceptions</option>
        <option v-for="a in addressAliases" :key="a.name" :value="a.name">{{ a.name }}</option>
      </select>
    </FormField>

    <p class="text-sm text-neutral-500">
      There is no per-client blocking policy: dnsmasq answers every client the same way. A client
      exempted here is not sent to this resolver at all, so it is not blocked either.
    </p>
  </div>
</template>
