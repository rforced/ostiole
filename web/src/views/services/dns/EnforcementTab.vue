<script setup>
import { computed } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const auth = useAuthStore()
const config = useConfigStore()
const enforce = computed(() => config.ensureBlocking().enforce)
const dnsOn = computed(() => Boolean(config.draft?.services?.dns?.enabled))

/** Aliases that hold addresses; port aliases are no use here. */
const addressAliases = computed(() => config.aliases.filter((a) => a.type !== 'ports'))

/** Published lists of the addresses public DoH resolvers answer on, IPv4 and IPv6. */
const DOH_LIST_URLS = [
  'https://raw.githubusercontent.com/dibdot/DoH-IP-blocklists/refs/heads/master/doh-ipv4.txt',
  'https://raw.githubusercontent.com/dibdot/DoH-IP-blocklists/refs/heads/master/doh-ipv6.txt',
]

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
const exemptClients = aliasField('exemptClients')
const exemptDestinations = aliasField('exemptDestinations')
</script>

<template>
  <div class="space-y-5">
    <AppNotice v-if="!dnsOn">
      The DNS server is off, so plain DNS cannot be sent here and Firefox is not answered. Turn it
      on under <RouterLink to="/services/dns" class="link">DNS › Resolver</RouterLink>. Dropping
      encrypted DNS works either way.
    </AppNotice>

    <SectionCard title="Keep clients here" :locked="auth.readOnly">
      <div class="space-y-3">
        <ToggleRow
          v-model="enforce.redirectDns"
          label="Send all plain DNS to this router"
          hint="Queries sent to any other resolver are answered by this router instead, on the interfaces it listens on."
        />
        <ToggleRow
          v-model="enforce.blockDot"
          label="Block DNS over TLS"
          hint="Everything to port 853 is refused."
        />
        <ToggleRow
          v-model="enforce.firefoxCanary"
          label="Ask Firefox not to turn on DNS over HTTPS"
          hint="Answers use-application-dns.net with NXDOMAIN, and Firefox stays on plain DNS."
        />
      </div>
    </SectionCard>

    <SectionCard title="Encrypted DNS" :locked="auth.readOnly">
      <div class="max-w-2xl space-y-4">
        <FormField
          id="enf-doh"
          label="Block traffic to DNS over HTTPS servers"
          hint="Make a fetched host alias under Firewall › Aliases and name it here."
        >
          <select id="enf-doh" v-model="dohAlias" class="input">
            <option value="">Not blocked</option>
            <option v-for="a in addressAliases" :key="a.name" :value="a.name">{{ a.name }}</option>
          </select>
        </FormField>
        <p class="text-ink-muted">
          Published lists of those addresses:
          <span v-for="url in DOH_LIST_URLS" :key="url" class="block font-mono break-all">{{
            url
          }}</span>
        </p>
        <FormField
          id="enf-exempt-clients"
          label="Except these clients"
          hint="Clients whose own address is in this alias are left alone by everything above. To let a blocked site through, use Except these destinations."
        >
          <select id="enf-exempt-clients" v-model="exemptClients" class="input">
            <option value="">None</option>
            <option v-for="a in addressAliases" :key="a.name" :value="a.name">{{ a.name }}</option>
          </select>
        </FormField>
        <FormField
          id="enf-exempt-destinations"
          label="Except these destinations"
          hint="Traffic to addresses in this alias is left alone by everything above. Keep it narrow: a cloud range lets every DoH server inside it through."
        >
          <select id="enf-exempt-destinations" v-model="exemptDestinations" class="input">
            <option value="">None</option>
            <option v-for="a in addressAliases" :key="a.name" :value="a.name">{{ a.name }}</option>
          </select>
        </FormField>
      </div>
    </SectionCard>
  </div>
</template>
