<script setup>
import { Plus } from 'lucide-vue-next'
import { ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import UsedByCell from '@/components/UsedByCell.vue'
import { adminOnly, useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import AccountDialog from '@/views/system/certificates/AccountDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const editing = ref(null)
const open = ref(false)

function add() {
  editing.value = null
  open.value = true
}
function edit(account) {
  editing.value = account
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="ACME accounts"
      :count="config.acmeAccounts.length"
      intro="One account per CA. The key is generated here and kept in the configuration."
      flush
    >
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" :disabled="!auth.isAdmin" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add account
        </button>
      </template>
      <table class="table table-stack">
        <thead>
          <tr>
            <th>Name</th>
            <th>Directory</th>
            <th>Email</th>
            <th>EAB</th>
            <th>Used by</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!config.acmeAccounts.length">
            <td colspan="6" class="text-ink-muted">No accounts.</td>
          </tr>
          <tr
            v-for="a in config.acmeAccounts"
            :key="a.id"
            :class="{ 'row-changed': config.isChanged('acme.accounts', a.id) }"
          >
            <td data-label="">
              <div class="font-mono text-code">{{ a.id }}</div>
              <div v-if="a.description" class="text-xs text-ink-muted">{{ a.description }}</div>
            </td>
            <td class="font-mono text-code break-all" data-label="Directory">{{ a.directory }}</td>
            <td data-label="Email">{{ a.email || '—' }}</td>
            <td data-label="EAB">{{ a.eabKeyId ? 'yes' : 'no' }}</td>
            <UsedByCell :names="config.accountDependents(a.id)" />
            <td class="actions" data-label="">
              <button type="button" class="link-action" @click="edit(a)">
                {{ auth.isAdmin ? 'Edit' : 'View' }}
              </button>
              <ConfirmButton
                label="Delete"
                :question="`Delete ACME account ${a.id}?`"
                description="Nothing is deleted at the CA."
                :typed="a.id"
                :disabled="!auth.isAdmin || config.accountDependents(a.id).length > 0"
                @confirm="config.removeAcmeAccount(a.id)"
              />
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="auth.isOperator" class="card-strip border-t border-line text-ink-muted">
        {{ adminOnly('change', 'ACME accounts') }}
      </div>
    </SectionCard>

    <AccountDialog v-model:open="open" :account="editing" />
  </div>
</template>
