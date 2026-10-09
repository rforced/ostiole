<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { ApiError, api } from '@/lib/api'
import { emptyText, errorMessage, useAsync } from '@/lib/async'
import { formatWhen } from '@/lib/format'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'
import AccountDialog from '@/views/system/accounts/AccountDialog.vue'
import { ROLES } from '@/views/system/accounts/roles'

/** The shortest password the server will take. */
const MIN_PASSWORD = 12

const auth = useAuthStore()
const confirm = useConfirmStore()
const users = ref([])
const actionError = ref('')
/** A viewer or an operator cannot see this card at all, which is the point. */
const available = ref(true)

const adding = ref(false)
/** The account a dialog is acting on, and the value being typed into it. */
const renaming = ref(null)
const renameTo = ref('')
const repassword = ref(null)
const newPassword = ref('')

const load = useAsync(async () => {
  try {
    users.value = await api.users.list()
    available.value = true
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) {
      available.value = false
      return
    }
    throw e
  }
})
onMounted(load.run)

const error = computed(() => actionError.value || load.error.value)

/**
 * Whether this row is the signed-in account. The server refuses to let an
 * account change its own role or delete itself; the UI says so up front
 * rather than waiting for the error.
 */
const isSelf = (u) => u.username === auth.user?.username

/** Every account action redraws from the list the server sent back. */
async function act(fn) {
  actionError.value = ''
  try {
    users.value = await fn()
    return true
  } catch (e) {
    actionError.value = errorMessage(e)
    await load.run()
    return false
  }
}

async function createUser(account) {
  if (await act(() => api.users.create(account))) adding.value = false
}

async function changeRole(u, event) {
  const role = event.target.value
  const noun = ROLES.find((r) => r.value === role)?.noun ?? role
  const ok = await confirm.ask({
    question: `Make ${u.username} ${noun}?`,
    description: 'Takes effect on their next request.',
    confirmLabel: 'Change role',
  })
  if (!ok) {
    event.target.value = u.role
    return
  }
  await act(() => api.users.setRole(u.username, role))
}

function openRename(u) {
  renaming.value = u
  renameTo.value = u.username
}

async function renameUser() {
  const was = renaming.value.username
  const self = isSelf(renaming.value)
  if (!(await act(() => api.users.rename(was, renameTo.value)))) return
  // A rename keeps the session, so the name in the header has to follow it.
  if (self) auth.user = await api.auth.me()
  renaming.value = null
}

function openPassword(u) {
  repassword.value = u
  newPassword.value = ''
}

async function setPassword() {
  const name = repassword.value.username
  const ok = await act(async () => {
    await api.users.setPassword(name, newPassword.value)
    return api.users.list()
  })
  if (ok) repassword.value = null
}

async function deleteUser(username) {
  await act(() => api.users.remove(username))
}
</script>

<template>
  <SectionCard v-if="available" title="Accounts" :count="users.length" flush>
    <template #intro>Roles decide what an account may do.</template>
    <template #actions>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
      <button type="button" class="btn-secondary" @click="adding = true">
        <Plus class="size-4" aria-hidden="true" /> Add account
      </button>
    </template>

    <div v-if="error" class="card-strip">
      <ErrorLine>{{ error }}</ErrorLine>
    </div>

    <table class="table table-stack">
      <thead>
        <tr>
          <th>Account</th>
          <th>Role</th>
          <th>Created</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!users.length">
          <td colspan="4" class="text-ink-muted">
            {{ emptyText(load, 'No accounts.') }}
          </td>
        </tr>
        <tr v-for="u in users" :key="u.username">
          <td class="font-mono" data-label="">
            {{ u.username }}
            <span v-if="isSelf(u)" class="ml-1 font-sans text-xs text-ink-muted">you</span>
          </td>
          <td data-label="Role">
            <select
              class="input w-48 max-sm:w-full"
              :value="u.role"
              :disabled="isSelf(u)"
              :aria-label="`Role for ${u.username}`"
              :aria-describedby="isSelf(u) ? 'role-self' : undefined"
              @change="changeRole(u, $event)"
            >
              <option v-for="r in ROLES" :key="r.value" :value="r.value">{{ r.label }}</option>
            </select>
            <p v-if="isSelf(u)" id="role-self" class="mt-1 text-ink-muted">
              Another admin has to change your role.
            </p>
          </td>
          <td class="when" data-label="Created">{{ formatWhen(u.createdAt) }}</td>
          <td class="actions" data-label="">
            <button type="button" class="link-action" @click="openRename(u)">Rename</button>
            <button v-if="!isSelf(u)" type="button" class="link-action" @click="openPassword(u)">
              Set password
            </button>
            <ConfirmButton
              v-if="!isSelf(u)"
              label="Delete"
              :question="`Delete account ${u.username}?`"
              description="Their sessions end immediately."
              :typed="u.username"
              @confirm="deleteUser(u.username)"
            />
          </td>
        </tr>
      </tbody>
    </table>

    <template #footer>
      You cannot change your own role or delete your own account. A forgotten password is fixed from
      the console with <span class="font-mono">ostiole reset-password</span>.
    </template>

    <AccountDialog v-model:open="adding" @create="createUser" />

    <AppDialog
      :open="renaming !== null"
      :title="`Rename ${renaming?.username}`"
      description="Keeps the role, the password, and any open sessions."
      @update:open="renaming = null"
    >
      <form id="rename-form" class="space-y-4" @submit.prevent="renameUser">
        <FormField id="rename-to" label="New username">
          <input
            id="rename-to"
            v-model="renameTo"
            class="input font-mono"
            autocomplete="off"
            required
          />
        </FormField>
      </form>
      <template #footer>
        <button type="button" class="btn-secondary" @click="renaming = null">Cancel</button>
        <button type="submit" form="rename-form" class="btn-primary" :disabled="!renameTo">
          Rename
        </button>
      </template>
    </AppDialog>

    <AppDialog
      :open="repassword !== null"
      :title="`Set a password for ${repassword?.username}`"
      description="Signs them out of every session."
      @update:open="repassword = null"
    >
      <form id="password-form" class="space-y-4" @submit.prevent="setPassword">
        <FormField
          id="reset-password"
          label="New password"
          :hint="`At least ${MIN_PASSWORD} characters.`"
        >
          <input
            id="reset-password"
            v-model="newPassword"
            type="password"
            autocomplete="new-password"
            class="input"
            :minlength="MIN_PASSWORD"
            required
          />
        </FormField>
      </form>
      <template #footer>
        <button type="button" class="btn-secondary" @click="repassword = null">Cancel</button>
        <button
          type="submit"
          form="password-form"
          class="btn-primary"
          :disabled="newPassword.length < MIN_PASSWORD"
        >
          Set password
        </button>
      </template>
    </AppDialog>
  </SectionCard>
</template>
