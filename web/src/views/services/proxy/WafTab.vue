<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import ProfileDialog from '@/views/services/proxy/ProfileDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const editing = ref(null)
const dialogOpen = ref(false)

const profiles = computed(() => config.proxy.wafProfiles ?? [])

function add() {
  editing.value = null
  dialogOpen.value = true
}

function edit(profile) {
  editing.value = profile
  dialogOpen.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="WAF profiles" :count="profiles.length" flush>
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add profile
        </button>
      </template>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Profile</th>
            <th>Mode</th>
            <th>Paranoia</th>
            <th>Applications</th>
            <th>Exclusions</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!profiles.length">
            <td colspan="6" class="text-ink-muted">No profiles.</td>
          </tr>
          <tr
            v-for="w in profiles"
            :key="w.id"
            :class="{ 'row-changed': config.isChanged('services.proxy.wafProfiles', w.id) }"
          >
            <td data-label="">
              <div class="font-mono font-medium">{{ w.id }}</div>
              <div v-if="w.description" class="text-xs text-ink-muted">{{ w.description }}</div>
            </td>
            <td data-label="Mode">
              <span class="badge" :class="w.mode === 'block' ? 'badge-bad' : 'badge-warn'">
                {{ w.mode === 'block' ? 'block' : 'detect only' }}
              </span>
            </td>
            <td class="tabular-nums" data-label="Paranoia">{{ w.paranoia || 1 }}</td>
            <td data-label="Applications">
              <span v-if="(w.applications ?? []).length">{{ w.applications.join(', ') }}</span>
              <span v-else class="text-ink-muted">none</span>
            </td>
            <td class="tabular-nums" data-label="Exclusions">{{ (w.exclusions ?? []).length }}</td>
            <td class="text-right whitespace-nowrap" data-label="">
              <button type="button" class="link" @click="edit(w)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete WAF profile ${w.id}?`"
                :dependents="config.profileDependents(w.id)"
                dependents-label="Left uninspected"
                :typed="w.id"
                @confirm="config.removeProfile(w.id)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <ProfileDialog v-model:open="dialogOpen" :profile="editing" />
  </div>
</template>
