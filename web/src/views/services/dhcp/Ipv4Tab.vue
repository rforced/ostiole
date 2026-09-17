<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { useConfigStore } from '@/stores/config'
import ScopeDialog from '@/views/services/dhcp/ScopeDialog.vue'
import StaticLeaseDialog from '@/views/services/dhcp/StaticLeaseDialog.vue'

const config = useConfigStore()
const dhcp = computed(() => config.ensureServices().dhcp)
const scopeEditing = ref(null)
const scopeOpen = ref(false)
const leaseEditing = ref(null)
const leaseOpen = ref(false)

function addScope() {
  scopeEditing.value = null
  scopeOpen.value = true
}
function editScope(s) {
  scopeEditing.value = s
  scopeOpen.value = true
}
function addLease() {
  leaseEditing.value = null
  leaseOpen.value = true
}
function editLease(l) {
  leaseEditing.value = l
  leaseOpen.value = true
}
</script>

<template>
  <div class="space-y-6">
    <label class="flex items-center gap-2 text-sm">
      <input v-model="dhcp.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
      <span class="font-medium">DHCP server enabled</span>
    </label>

    <section class="space-y-3" aria-labelledby="scopes-title">
      <div class="flex items-center gap-3">
        <h2 id="scopes-title" class="section-title">Scopes</h2>
        <button type="button" class="btn-secondary" @click="addScope">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add scope
        </button>
      </div>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Interface</th>
              <th>Range</th>
              <th>Lease</th>
              <th>Gateway</th>
              <th>DNS</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!(dhcp.scopes ?? []).length" key="empty">
              <td colspan="6" class="text-neutral-500">
                No scopes. Add one per interface that should hand out addresses.
              </td>
            </tr>
            <tr
              v-for="s in dhcp.scopes"
              :key="s.interface"
              :class="{
                'opacity-50': !s.enabled,
                'row-changed': config.isChanged('services.dhcp.scopes', s.interface),
              }"
            >
              <td class="font-mono">{{ s.interface }}</td>
              <td class="font-mono text-code">{{ s.rangeStart }} – {{ s.rangeEnd }}</td>
              <td class="font-mono text-code">{{ s.leaseTime || '12h' }}</td>
              <td class="font-mono text-code">{{ s.gateway || 'this box' }}</td>
              <td class="font-mono text-code">{{ s.dns?.join(', ') || 'this box' }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editScope(s)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete the DHCP scope on ${s.interface}?`"
                  @confirm="config.removeScope(s.interface)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </div>
    </section>

    <section class="space-y-3" aria-labelledby="leases-title">
      <div class="flex items-center gap-3">
        <h2 id="leases-title" class="section-title">Static leases</h2>
        <button type="button" class="btn-secondary" @click="addLease">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add static lease
        </button>
      </div>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>MAC</th>
              <th>IPv4</th>
              <th>IPv6</th>
              <th>Hostname</th>
              <th>Description</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!(dhcp.staticLeases ?? []).length" key="empty">
              <td colspan="6" class="text-neutral-500">No static leases.</td>
            </tr>
            <tr
              v-for="l in dhcp.staticLeases"
              :key="l.mac"
              :class="{ 'row-changed': config.isChanged('services.dhcp.staticLeases', l.mac) }"
            >
              <td class="font-mono text-code">{{ l.mac }}</td>
              <td class="font-mono text-code">{{ l.ip || '—' }}</td>
              <td class="font-mono text-code">{{ l.ipv6 || '—' }}</td>
              <td class="font-mono text-code">{{ l.hostname }}</td>
              <td>{{ l.description }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editLease(l)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete the static lease for ${l.mac}?`"
                  :description="l.description"
                  @confirm="config.removeStaticLease(l.mac)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </div>
    </section>

    <ScopeDialog v-model:open="scopeOpen" :scope="scopeEditing" />
    <StaticLeaseDialog v-model:open="leaseOpen" :lease="leaseEditing" />
  </div>
</template>
