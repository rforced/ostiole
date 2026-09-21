<script setup>
import { computed } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useConfigStore } from '@/stores/config'

const config = useConfigStore()
const enforce = computed(() => config.ensureBlocking().enforce)
const dnsOn = computed(() => Boolean(config.draft?.services?.dns?.enabled))

/** Aliases that hold addresses; port aliases are no use here. */
const addressAliases = computed(() => config.aliases.filter((a) => a.type !== 'ports'))

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
  <div class="space-y-5">
    <AppNotice v-if="!dnsOn">
      The DNS server is off, so plain DNS cannot be sent here and Firefox is not answered. Turn it
      on under Resolver. Dropping encrypted DNS works either way.
    </AppNotice>

    <SectionCard title="Keep clients here">
      <div class="space-y-3">
        <ToggleRow
          v-model="enforce.redirectDns"
          label="Send all plain DNS to this router"
          hint="Queries sent to any other resolver are answered by this router instead."
        />
        <ToggleRow
          v-model="enforce.blockDot"
          label="Drop DNS over TLS"
          hint="Everything to port 853 is dropped."
        />
        <ToggleRow
          v-model="enforce.firefoxCanary"
          label="Ask Firefox not to turn on DNS over HTTPS"
          hint="Answers use-application-dns.net with NXDOMAIN, and Firefox stays on plain DNS."
        />
      </div>
    </SectionCard>

    <SectionCard title="Encrypted DNS">
      <div class="max-w-2xl space-y-4">
        <FormField
          id="enf-doh"
          label="Drop traffic to DNS over HTTPS servers"
          hint="Make a fetched host alias under Firewall, Aliases and name it here."
        >
          <select id="enf-doh" v-model="dohAlias" class="input">
            <option value="">Not blocked</option>
            <option v-for="a in addressAliases" :key="a.name" :value="a.name">{{ a.name }}</option>
          </select>
        </FormField>
        <p class="text-ink-muted">
          A published list of those addresses:
          <span class="font-mono break-all">{{ DOH_LIST_URL }}</span>
        </p>
        <FormField
          id="enf-exempt"
          label="Except these clients"
          hint="Addresses in this alias are left alone by everything above, so nothing is blocked for them."
        >
          <select id="enf-exempt" v-model="exemptAlias" class="input">
            <option value="">No exceptions</option>
            <option v-for="a in addressAliases" :key="a.name" :value="a.name">{{ a.name }}</option>
          </select>
        </FormField>
      </div>
    </SectionCard>
  </div>
</template>
