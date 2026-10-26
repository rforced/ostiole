<script setup lang="ts">
import { TabsContent } from 'reka-ui'
import { ref } from 'vue'

import AppTabs from '@/components/AppTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { usePageTabs } from '@/lib/tabs'
import TunnelsTab from '@/views/vpn/openvpn/TunnelsTab.vue'
import type { GatewayStatus, OvpnLive, OvpnStatus } from '@/views/vpn/types'

/** How often the tunnels are read while the page is shown. */
const POLL_MS = 5000

const { tabs, tab } = usePageTabs()

/** Each tunnel as its daemon has it, by name. */
const live = ref(new Map<string, OvpnLive>())
/** Whether this router has OpenVPN set up; assumed until the first read says otherwise. */
const setUp = ref(true)
/** Whether the kernel module was left out because Secure Boot is on. */
const secureBootSkipped = ref(false)
/** The gateway monitor's word on each gateway, for the tunnels one sends traffic into. */
const gateways = ref<GatewayStatus[]>([])
const read = useAsync(
  async () => {
    const got = (await api.openvpn.status()) as OvpnStatus
    live.value = new Map((got.tunnels ?? []).map((t) => [t.name, t]))
    setUp.value = got.setUp !== false
    secureBootSkipped.value = Boolean(got.secureBootSkipped)
    try {
      gateways.value = (await api.gateways()) as GatewayStatus[]
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
        <TunnelsTab
          :live="live"
          :set-up="setUp"
          :secure-boot-skipped="secureBootSkipped"
          :gateways="gateways"
          :read="read"
        />
      </TabsContent>
    </AppTabs>
  </div>
</template>
