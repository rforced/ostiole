<script setup>
import { Plus } from 'lucide-vue-next'
import { ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { someOf } from '@/lib/lists'
import { useConfigStore } from '@/stores/config'
import AccountDialog from '@/views/system/certificates/AccountDialog.vue'

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
      <template #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add account
        </button>
      </template>
      <table class="table">
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
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!config.acmeAccounts.length" key="empty" class="row-static">
            <td colspan="6" class="text-ink-muted">No accounts.</td>
          </tr>
          <tr
            v-for="a in config.acmeAccounts"
            :key="a.id"
            :class="{ 'row-changed': config.isChanged('acme.accounts', a.id) }"
          >
            <td>
              <div class="font-mono text-code">{{ a.id }}</div>
              <div v-if="a.description" class="text-ink-muted">{{ a.description }}</div>
            </td>
            <td class="font-mono text-code break-all">{{ a.directory }}</td>
            <td>{{ a.email || '—' }}</td>
            <td>{{ a.eabKeyId ? 'yes' : 'no' }}</td>
            <td class="font-mono text-code">
              {{ config.accountDependents(a.id).join(', ') || '—' }}
            </td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="edit(a)">Edit</button>
              <ConfirmButton
                v-if="config.accountDependents(a.id).length === 0"
                class="ml-3"
                label="Delete"
                :question="`Delete ACME account ${a.id}?`"
                description="Nothing is deleted at the CA."
                :typed="a.id"
                @confirm="config.removeAcmeAccount(a.id)"
              />
              <span
                v-else
                class="ml-3 text-sm text-ink-muted"
                :title="config.accountDependents(a.id).join(', ')"
              >
                In use by {{ someOf(config.accountDependents(a.id)) }}
              </span>
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <AccountDialog v-model:open="open" :account="editing" />
  </div>
</template>
