<script setup>
import { computed } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import { useServicesStatus } from '@/lib/servicesStatus'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** Which service the page is about: dhcp, dns, or upnp. */
  service: { type: String, default: 'dhcp' },
})

const config = useConfigStore()
const { status } = useServicesStatus(props.service, { load: true })

const isDns = computed(() => props.service === 'dns')

/** The draft asks for the validating resolver (DNSSEC or DNS over TLS). */
const resolverWanted = computed(() => {
  const dns = config.draft?.services?.dns
  return Boolean(dns?.enabled && dns.resolver && dns.resolver !== 'forward')
})

/** The draft asks for port mapping, and something is there to answer. */
const upnpWanted = computed(() => {
  const u = config.draft?.services?.upnp
  return Boolean(u?.enabled && (u.igd || u.pcp))
})
</script>

<template>
  <div v-if="status && config.loaded" class="space-y-3 empty:hidden">
    <template v-if="service === 'upnp'">
      <AppNotice v-if="!status.upnpSetUp && upnpWanted">
        UPnP is not set up on this router yet. Run
        <code class="font-mono">ostiole repair</code> as root once. Until then, applying this fails.
      </AppNotice>
    </template>
    <template v-else>
      <AppNotice v-if="!status.setUp">
        DHCP and DNS are not set up on this router yet. Run
        <code class="font-mono">ostiole repair</code> as root once. Until then, enabling either
        fails to apply.
      </AppNotice>
      <AppNotice v-if="isDns && status.resolverUnreachable?.length">
        No answer on port 853 from
        <span class="font-mono">{{ status.resolverUnreachable.join(', ') }}</span
        >. Where that port is blocked every name fails. Validate or Forward would still resolve.
      </AppNotice>
      <AppNotice v-if="isDns && !status.resolverSetUp && resolverWanted">
        The validating resolver is not installed. Run
        <code class="font-mono">ostiole repair</code> as root once. Until then, applying this DNS
        configuration fails.
      </AppNotice>
    </template>
  </div>
</template>
