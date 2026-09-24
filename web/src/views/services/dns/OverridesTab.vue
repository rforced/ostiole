<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useDraftRows } from '@/lib/draft'
import { overrideKey, overrideName } from '@/lib/hosts'
import { useConfigStore } from '@/stores/config'
import DomainOverrideDialog from '@/views/services/dns/DomainOverrideDialog.vue'
import HostOverrideDialog from '@/views/services/dns/HostOverrideDialog.vue'

const config = useConfigStore()
const dns = computed(() => config.ensureServices().dns)
const hosts = computed(() => dns.value.hostOverrides ?? [])
const domains = computed(() => dns.value.domainOverrides ?? [])

/**
 * The names the router answers that nobody wrote on this page: static
 * leases with hostnames. Read for the draft, like the system rules, so a
 * lease added a moment ago shows here before it is applied.
 */
const { rows: system, error: systemError } = useDraftRows((draft) => api.systemHosts(draft))

const hostEditing = ref(null)
const hostOpen = ref(false)
function addHost() {
  hostEditing.value = null
  hostOpen.value = true
}
function editHost(h) {
  hostEditing.value = h
  hostOpen.value = true
}

const domainEditing = ref(null)
const domainOpen = ref(false)
function addDomain() {
  domainEditing.value = null
  domainOpen.value = true
}
function editDomain(d) {
  domainEditing.value = d
  domainOpen.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Host overrides" :count="hosts.length" flush>
      <template #intro>
        <template v-if="dns.domain">
          A name with no domain lives under <span class="font-mono">{{ dns.domain }}</span> and
          answers bare as well; one with its own domain answers in full only. The first name on a
          row answers the reverse lookup.
        </template>
        <template v-else>The first name on a row answers the reverse lookup.</template>
      </template>
      <template #actions>
        <button type="button" class="btn-secondary" @click="addHost">
          <Plus class="size-4" aria-hidden="true" /> Add host
        </button>
      </template>
      <table class="table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Address</th>
            <th>Aliases</th>
            <th>Description</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!hosts.length" key="empty" class="row-static">
            <td colspan="5" class="text-ink-muted">No overrides.</td>
          </tr>
          <tr
            v-for="h in hosts"
            :key="overrideKey(h)"
            :class="{
              'row-changed': config.isChanged('services.dns.hostOverrides', overrideKey(h)),
            }"
          >
            <td class="font-mono text-code">{{ overrideName(h, dns.domain) }}</td>
            <td class="font-mono text-code">{{ h.ip }}</td>
            <td class="font-mono text-code">{{ (h.aliases ?? []).join(', ') }}</td>
            <td>{{ h.description }}</td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="editHost(h)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete the host override for ${overrideName(h, dns.domain)}?`"
                :description="h.description"
                @confirm="config.removeHostOverride(overrideKey(h))"
              />
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <SectionCard
      title="From static leases"
      intro="A static lease with a hostname is answered like an override. Names clients send with their requests resolve too; those are on the DHCP leases tab."
      flush
    >
      <template #actions>
        <RouterLink to="/services/dhcp" class="link">Change under DHCP</RouterLink>
      </template>
      <p v-if="systemError" role="alert" class="card-strip text-bad">
        {{ systemError }}
      </p>
      <table class="table">
        <thead>
          <tr>
            <th>Hostname</th>
            <th>Address</th>
            <th>Lease</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!system.length" class="row-static">
            <td colspan="4" class="text-ink-muted">No static lease has a hostname.</td>
          </tr>
          <tr v-for="s in system" :key="`${s.hostname}-${s.ip}`" class="row-static">
            <td class="font-mono text-code">
              {{ s.hostname }}
              <span v-if="s.fqdn" class="ml-1 text-ink-muted">{{ s.fqdn }}</span>
            </td>
            <td class="font-mono text-code">{{ s.ip }}</td>
            <td>
              <span class="font-mono text-code">{{ s.mac }}</span>
              <span v-if="s.description" class="ml-1 text-ink-muted">{{ s.description }}</span>
            </td>
            <td class="text-right whitespace-nowrap">
              <span class="badge">locked</span>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <SectionCard
      title="Domain overrides"
      :count="domains.length"
      intro="A domain here goes to its own resolvers, and stops answering while they are unreachable."
      flush
    >
      <template #actions>
        <button type="button" class="btn-secondary" @click="addDomain">
          <Plus class="size-4" aria-hidden="true" /> Add domain
        </button>
      </template>
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
          <tr v-if="!domains.length" key="empty" class="row-static">
            <td colspan="4" class="text-ink-muted">
              No overrides. Every domain goes to the resolver.
            </td>
          </tr>
          <tr
            v-for="d in domains"
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
    </SectionCard>

    <HostOverrideDialog v-model:open="hostOpen" :override="hostEditing" />
    <DomainOverrideDialog v-model:open="domainOpen" :override="domainEditing" />
  </div>
</template>
