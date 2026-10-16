<script setup>
import { Plus } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'

import ActionButton from '@/components/ActionButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { api } from '@/lib/api'
import { emptyText, errorMessage, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import * as webauthn from '@/lib/webauthn'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'
import PasskeyDialog from '@/views/account/PasskeyDialog.vue'
import PasskeyRenameDialog from '@/views/account/PasskeyRenameDialog.vue'
import RemoveAppDialog from '@/views/account/RemoveAppDialog.vue'
import TotpDialog from '@/views/account/TotpDialog.vue'

const auth = useAuthStore()
const confirm = useConfirmStore()
const state = ref(null)
const actionError = ref('')
const settingUp = ref(false)
const adding = ref(false)
const removingApp = ref(false)
/** The passkey the rename dialog is acting on. */
const renaming = ref(null)
const renameOpen = ref(false)
/** The id of the passkey being removed. */
const removing = ref('')

const load = useAsync(async () => {
  state.value = await api.auth.mfa()
})
onMounted(load.run)

const error = computed(() => actionError.value || load.error.value)
const totp = computed(() => state.value?.totp ?? null)
const passkeys = computed(() => state.value?.passkeys ?? [])
const rpId = computed(() => state.value?.rpId ?? '')

const intro = computed(() => {
  if (!state.value) return ''
  if (!totp.value) return 'Sign-in asks for your password alone.'
  if (passkeys.value.length)
    return 'Sign-in takes a passkey on its own, or your password and then a code from your authenticator app or a passkey.'
  return 'Sign-in asks for a code from your authenticator app after your password.'
})

/** Why a passkey cannot be added now, or empty when it can. */
const passkeyBlock = computed(() => {
  if (!state.value) return ''
  if (!totp.value) return 'Set up an authenticator app first.'
  if (!rpId.value) return 'Open the UI by its host name to add a passkey.'
  if (!webauthn.available()) return 'This browser cannot make passkeys on this page.'
  return ''
})

/** A time never set may come as Go's zero time. */
const used = (t) => (t?.startsWith('0001-') ? '' : t)

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

function openRename(p) {
  renaming.value = p
  renameOpen.value = true
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
  <SectionCard title="Multi-factor authentication" flush>
    <template v-if="intro" #intro>{{ intro }}</template>
    <template #actions>
      <StatusBadge v-if="state" :state="totp ? 'on' : 'off'" />
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    </template>

    <div v-if="error || !state" class="card-strip">
      <ErrorLine v-if="error">{{ error }}</ErrorLine>
      <p v-else class="text-ink-muted">Reading…</p>
    </div>

    <template v-if="state">
      <fieldset class="field-group card-strip">
        <legend class="float-left">Authenticator app</legend>
        <div class="clear-left flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
          <template v-if="totp">
            <dl class="flex flex-wrap gap-x-6 gap-y-1">
              <div class="flex items-baseline gap-1.5">
                <dt class="text-xs text-ink-muted">Set up</dt>
                <dd class="when">{{ formatWhen(totp.enabledAt) }}</dd>
              </div>
              <div class="flex items-baseline gap-1.5">
                <dt class="text-xs text-ink-muted">Last used</dt>
                <dd class="when">{{ formatWhen(used(totp.lastUsed), 'never') }}</dd>
              </div>
            </dl>
            <div class="flex gap-3">
              <button type="button" class="link-action" @click="settingUp = true">Replace</button>
              <button type="button" class="link-action" @click="removingApp = true">Remove</button>
            </div>
          </template>
          <template v-else>
            <p class="text-ink-muted">Not set up.</p>
            <button type="button" class="btn-secondary" @click="settingUp = true">
              Set up authenticator app
            </button>
          </template>
        </div>
      </fieldset>

      <fieldset class="field-group border-t border-line pt-3">
        <legend class="float-left mb-0 px-4 py-1.5">Passkeys</legend>
        <div class="flex justify-end px-4">
          <button
            type="button"
            class="btn-secondary"
            :disabled="Boolean(passkeyBlock)"
            :aria-describedby="passkeyBlock ? 'passkey-block' : undefined"
            @click="adding = true"
          >
            <Plus class="size-4" aria-hidden="true" /> Add passkey
          </button>
        </div>
        <p v-if="passkeyBlock" id="passkey-block" class="clear-left px-4 text-ink-muted">
          {{ passkeyBlock }}
        </p>

        <table class="table table-stack clear-left">
          <thead>
            <tr>
              <th>Passkey</th>
              <th>Host name</th>
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
              <td class="font-mono text-code" data-label="Host name">{{ p.rpId }}</td>
              <td class="when" data-label="Added">{{ formatWhen(p.createdAt) }}</td>
              <td class="when" data-label="Last used">
                {{ formatWhen(used(p.lastUsed), 'never') }}
              </td>
              <td class="actions" data-label="">
                <button type="button" class="link-action" @click="openRename(p)">Rename</button>
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
      </fieldset>
    </template>

    <template #footer>
      Locked out? Run <span class="font-mono">ostiole mfa bypass</span> on the router.
    </template>

    <TotpDialog v-model:open="settingUp" :replacing="Boolean(totp)" @done="changed" />
    <PasskeyDialog v-model:open="adding" :rp-id="rpId" @done="changed" />
    <PasskeyRenameDialog v-model:open="renameOpen" :passkey="renaming" @done="changed" />
    <RemoveAppDialog
      v-model:open="removingApp"
      :passkeys="passkeys.map((p) => p.name)"
      @done="changed"
    />
  </SectionCard>
</template>
