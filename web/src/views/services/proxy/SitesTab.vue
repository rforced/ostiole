<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import SiteDialog from '@/views/services/proxy/SiteDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const editing = ref(null)
const dialogOpen = ref(false)

const sites = computed(() => config.proxy.sites ?? [])
const pools = computed(() => config.proxy.pools ?? [])

function certificateOf(site) {
  if (!site.certificate) return 'built-in'
  return site.certificate
}

function add() {
  editing.value = null
  dialogOpen.value = true
}

function edit(site) {
  editing.value = site
  dialogOpen.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Sites" :count="sites.length" flush>
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" :disabled="!pools.length" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add site
        </button>
      </template>
      <div v-if="!pools.length" class="card-strip text-ink-muted">Add a pool first.</div>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Site</th>
            <th>Hostnames</th>
            <th>Certificate</th>
            <th>Pool</th>
            <th>WAF</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!sites.length">
            <td colspan="6" class="text-ink-muted">No sites.</td>
          </tr>
          <tr
            v-for="s in sites"
            :key="s.id"
            :class="{ 'row-changed': config.isChanged('services.proxy.sites', s.id) }"
          >
            <td data-label="">
              <div class="font-mono font-medium">
                {{ s.id
                }}<span v-if="!s.enabled" class="ml-1 font-sans text-ink-muted">(disabled)</span>
              </div>
              <div v-if="s.description" class="text-xs text-ink-muted">{{ s.description }}</div>
            </td>
            <td class="font-mono text-code" data-label="Hostnames">
              {{ (s.hosts ?? []).join(', ') }}
            </td>
            <td class="font-mono text-code" data-label="Certificate">{{ certificateOf(s) }}</td>
            <td class="font-mono text-code" data-label="Pool">{{ s.pool }}</td>
            <td data-label="WAF">
              <span v-if="s.waf" class="font-mono text-code">{{ s.waf }}</span>
              <span v-else class="text-ink-muted">none</span>
            </td>
            <td class="text-right whitespace-nowrap" data-label="">
              <button type="button" class="link" @click="edit(s)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete site ${s.id}?`"
                :description="(s.hosts ?? []).join(', ')"
                :dependents="config.siteDependents(s.id)"
                :typed="s.id"
                @confirm="config.removeSite(s.id)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <SiteDialog v-model:open="dialogOpen" :site="editing" />
  </div>
</template>
