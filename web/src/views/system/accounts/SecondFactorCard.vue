<script setup>
import { Plus } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import AppNotice from '@/components/AppNotice.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, errorMessage, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import * as webauthn from '@/lib/webauthn'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'
import PasskeyDialog from '@/views/system/accounts/PasskeyDialog.vue'
import TotpDialog from '@/views/system/accounts/TotpDialog.vue'
import TurnOffDialog from '@/views/system/accounts/TurnOffDialog.vue'

const auth = useAuthStore()
const confirm = useConfirmStore()
const state = ref(null)
const actionError = ref('')
const settingUp = ref(false)
const adding = ref(false)
const turningOff = ref(false)
/** The id of the passkey being removed. */
const removing = ref('')

const load = useAsync(async () => {
  state.value = await api.auth.secondFactor()
})
onMounted(load.run)

const error = computed(() => actionError.value || load.error.value)
const totp = computed(() => state.value?.totp ?? false)
const passkeys = computed(() => state.value?.passkeys ?? [])
const rpId = computed(() => state.value?.rpId ?? '')

const status = computed(() => {
  if (!totp.value) return 'Off. Sign-in asks for your password alone.'
  return `On. Sign-in asks for a code from your authenticator app${passkeys.value.length ? ', or a passkey' : ''}.`
})

/** Why a passkey cannot be added now, or empty when it can. */
const passkeyBlock = computed(() => {
  if (!state.value) return ''
  if (!totp.value) return 'Passkeys need an authenticator app first.'
  if (!rpId.value) return 'Passkeys need a name in the address bar, not an address.'
  if (!webauthn.available()) return 'This browser cannot make passkeys on this page.'
  return ''
})

const bypass = computed(() => {
  const until = state.value?.bypassUntil
  if (!until) return ''
  const by = state.value.bypassBy ? ` by ${state.value.bypassBy}` : ''
  return `The second factor is bypassed until ${formatWhen(until)}${by}, from the command line.`
})

/** A passkey never used may come as Go's zero time. */
const lastUsed = (p) => (p.lastUsed?.startsWith('0001-') ? '' : p.lastUsed)

/** What the card shows now, and the session's own copy of it. */
async function changed() {
  actionError.value = ''
  await load.run()
  try {
    auth.user = await api.auth.me()
  } catch {
    /* the card already shows the new state */
  }
}

async function remove(p) {
  const ok = await confirm.ask({
    question: `Remove the passkey ${p.name}?`,
    description: 'It can no longer be used to sign in.',
    confirmLabel: 'Remove',
  })
  if (!ok) return
  actionError.value = ''
  removing.value = p.id
  try {
    await api.auth.removePasskey(p.id)
    await changed()
  } catch (e) {
    actionError.value = errorMessage(e)
  } finally {
    removing.value = ''
  }
}
</script>

<template>
  <SectionCard title="Second factor" flush>
    <template #actions>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
      <button type="button" class="btn-secondary" :disabled="!state" @click="settingUp = true">
        {{ totp ? 'Replace authenticator app' : 'Set up authenticator app' }}
      </button>
      <button
        type="button"
        class="btn-secondary"
        :disabled="!state || Boolean(passkeyBlock)"
        :aria-describedby="passkeyBlock ? 'passkey-block' : undefined"
        @click="adding = true"
      >
        <Plus class="size-4" aria-hidden="true" /> Add a passkey
      </button>
      <button v-if="totp" type="button" class="btn-secondary" @click="turningOff = true">
        Turn off
      </button>
    </template>

    <div class="card-strip space-y-2">
      <p v-if="state">{{ status }}</p>
      <p v-else-if="!error" class="text-ink-muted">Reading…</p>
      <p v-if="passkeyBlock" id="passkey-block" class="text-ink-muted">{{ passkeyBlock }}</p>
      <AppNotice v-if="bypass" kind="warn">{{ bypass }}</AppNotice>
      <ErrorLine v-if="error">{{ error }}</ErrorLine>
    </div>

    <table v-if="totp" class="table table-stack">
      <thead>
        <tr>
          <th>Passkey</th>
          <th>Made on</th>
          <th>Added</th>
          <th>Last used</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!passkeys.length">
          <td colspan="5" class="text-ink-muted">{{ emptyText(load, 'No passkeys.') }}</td>
        </tr>
        <tr v-for="p in passkeys" :key="p.id">
          <td data-label="">{{ p.name }}</td>
          <td class="font-mono text-code" data-label="Made on">{{ p.rpId }}</td>
          <td class="when" data-label="Added">{{ formatWhen(p.createdAt) }}</td>
          <td class="when" data-label="Last used">{{ formatWhen(lastUsed(p), 'never') }}</td>
          <td class="actions" data-label="">
            <ActionButton
              kind="link"
              label="Remove"
              busy-label="Removing…"
              :busy="removing === p.id"
              @click="remove(p)"
            />
          </td>
        </tr>
      </tbody>
    </table>

    <TotpDialog v-model:open="settingUp" :replacing="totp" @done="changed" />
    <PasskeyDialog v-model:open="adding" :rp-id="rpId" @done="changed" />
    <TurnOffDialog v-model:open="turningOff" @done="changed" />
  </SectionCard>
</template>
