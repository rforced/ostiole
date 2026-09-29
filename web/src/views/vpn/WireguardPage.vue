<script setup>
import { TabsContent } from 'reka-ui'
import { ref } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { usePageTabs } from '@/lib/tabs'
import PeerLogTab from '@/views/vpn/PeerLogTab.vue'
import PeersTab from '@/views/vpn/wireguard/PeersTab.vue'
import TunnelsTab from '@/views/vpn/wireguard/TunnelsTab.vue'

/** How often the tunnels are read while the page is shown. */
const POLL_MS = 5000

const { tabs, tab } = usePageTabs()

/** Each tunnel of the running configuration as its device has it, by name. */
const live = ref(new Map())
/** The gateway monitor's word on each gateway, for the tunnels one sends traffic into. */
const gateways = ref([])
const read = useAsync(
  async () => {
    const got = await api.wireguard.status()
    live.value = new Map(got.map((t) => [t.name, t]))
    // A monitor that is not running leaves only the gateway column bare.
    try {
      gateways.value = await api.gateways()
    } catch {
      gateways.value = []
    }
  },
  { interval: POLL_MS, immediate: true },
)
</script>

<template>
  <div class="space-y-5">
    <PageHeader />
    <AppTabs v-model="tab" :tabs="tabs">
      <TabsContent value="tunnels">
        <TunnelsTab :live="live" :gateways="gateways" :read="read" />
      </TabsContent>
      <TabsContent value="peers"><PeersTab :live="live" :read="read" /></TabsContent>
      <TabsContent value="log"><PeerLogTab kind="wireguard" /></TabsContent>
    </AppTabs>
  </div>
</template>
