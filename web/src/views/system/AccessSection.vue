<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { ApiError, api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { useAuthStore } from '@/stores/auth'
import { useConfirmStore } from '@/stores/confirm'

const MIN_PASSWORD = 12

const auth = useAuthStore()
const confirm = useConfirmStore()
const users = ref([])
const tokens = ref([])
const actionError = ref('')
const available = ref(true)
/** The one moment a new token's secret exists outside the server. */
const minted = ref(null)
const creating = ref(false)
const form = ref({ name: '', role: 'viewer', expiresInDays: 0 })

const addingUser = ref(false)
const newUser = ref({ username: '', password: '', role: 'viewer' })
/** The account a dialog is acting on, and the value being typed into it. */
const renaming = ref(null)
const renameTo = ref('')
const repassword = ref(null)
const newPassword = ref('')

const ROLES = [
  { value: 'admin', label: 'Admin: everything, including accounts and updates', noun: 'an admin' },
  { value: 'operator', label: 'Operator: change and apply the configuration', noun: 'an operator' },
  { value: 'viewer', label: 'Viewer: read only', noun: 'a viewer' },
]

const load = useAsync(async () => {
  try {
    ;[users.value, tokens.value] = await Promise.all([api.users.list(), api.tokens.list()])
    available.value = true
  } catch (e) {
    // A viewer or an operator cannot see this section at all, which is
    // the point of it.
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

const newUserValid = computed(
  () => newUser.value.username !== '' && newUser.value.password.length >= MIN_PASSWORD,
)

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

async function createUser() {
  if (await act(() => api.users.create(newUser.value))) {
    newUser.value = { username: '', password: '', role: 'viewer' }
    addingUser.value = false
  }
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

async function createToken() {
  actionError.value = ''
  try {
    minted.value = await api.tokens.create(form.value)
    form.value = { name: '', role: 'viewer', expiresInDays: 0 }
    creating.value = false
    await load.run()
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}

async function deleteToken(id) {
  actionError.value = ''
  try {
    await api.tokens.remove(id)
    await load.run()
  } catch (e) {
    actionError.value = errorMessage(e)
  }
}

const when = (s, fallback) => (s ? new Date(s).toLocaleDateString() : fallback)
</script>

<template>
  <section v-if="available" class="card space-y-4" aria-labelledby="access-title">
    <div class="flex items-center justify-between gap-4">
      <h2 id="access-title" class="card-title">Accounts and API tokens</h2>
      <RefreshButton :busy="load.busy.value" :updated-at="load.updatedAt.value" @click="load.run" />
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <div class="space-y-2">
      <div class="flex items-center gap-3">
        <h3 class="subsection-title">Accounts</h3>
        <button type="button" class="btn-secondary" @click="addingUser = !addingUser">
          <Plus class="mr-1 size-4" aria-hidden="true" /> New account
        </button>
      </div>

      <form v-if="addingUser" class="grid gap-3 sm:grid-cols-3" @submit.prevent="createUser">
        <FormField id="user-name" label="Username">
          <input
            id="user-name"
            v-model="newUser.username"
            class="input font-mono"
            autocomplete="off"
            required
            placeholder="operator"
          />
        </FormField>
        <FormField
          id="user-password"
          label="Password"
          :hint="`At least ${MIN_PASSWORD} characters.`"
        >
          <input
            id="user-password"
            v-model="newUser.password"
            type="password"
            autocomplete="new-password"
            class="input"
            :minlength="MIN_PASSWORD"
            required
          />
        </FormField>
        <FormField id="user-role" label="Role">
          <div class="flex gap-2">
            <select id="user-role" v-model="newUser.role" class="input">
              <option v-for="r in ROLES" :key="r.value" :value="r.value">{{ r.label }}</option>
            </select>
            <button type="submit" class="btn-primary" :disabled="!newUserValid">Create</button>
          </div>
        </FormField>
      </form>

      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Account</th>
              <th>Role</th>
              <th>Created</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!users.length" key="empty" class="row-static">
              <td colspan="4" class="text-neutral-500">
                {{ load.updatedAt.value ? 'No accounts.' : 'Reading accounts…' }}
              </td>
            </tr>
            <tr v-for="u in users" :key="u.username">
              <td class="font-mono">
                {{ u.username }}
                <span v-if="isSelf(u)" class="ml-1 font-sans text-xs text-neutral-500">you</span>
              </td>
              <td>
                <select
                  class="input w-72"
                  :value="u.role"
                  :disabled="isSelf(u)"
                  :aria-label="`Role for ${u.username}`"
                  :title="isSelf(u) ? 'Another admin has to change your role.' : undefined"
                  @change="changeRole(u, $event)"
                >
                  <option v-for="r in ROLES" :key="r.value" :value="r.value">{{ r.label }}</option>
                </select>
              </td>
              <td>{{ when(u.createdAt, '—') }}</td>
              <td class="space-x-3 text-right whitespace-nowrap">
                <button type="button" class="link-action" @click="openRename(u)">Rename</button>
                <button
                  v-if="!isSelf(u)"
                  type="button"
                  class="link-action"
                  @click="openPassword(u)"
                >
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
          </TransitionGroup>
        </table>
      </div>
      <p class="text-sm text-neutral-500">
        You cannot change your own role or delete your own account. A forgotten password is fixed
        from the console with <span class="font-mono">ostiole reset-password</span>.
      </p>
    </div>

    <AppDialog
      :open="renaming !== null"
      :title="`Rename ${renaming?.username}`"
      description="Keeps the role, the password, and any open sessions."
      @update:open="renaming = null"
    >
      <form class="space-y-3" @submit.prevent="renameUser">
        <FormField id="rename-to" label="New username">
          <input
            id="rename-to"
            v-model="renameTo"
            class="input font-mono"
            autocomplete="off"
            required
          />
        </FormField>
        <div class="flex gap-2">
          <button type="submit" class="btn-primary" :disabled="!renameTo">Rename</button>
          <button type="button" class="btn-secondary" @click="renaming = null">Cancel</button>
        </div>
      </form>
    </AppDialog>

    <AppDialog
      :open="repassword !== null"
      :title="`Set a password for ${repassword?.username}`"
      description="Signs them out of every session."
      @update:open="repassword = null"
    >
      <form class="space-y-3" @submit.prevent="setPassword">
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
        <div class="flex gap-2">
          <button type="submit" class="btn-primary" :disabled="newPassword.length < MIN_PASSWORD">
            Set password
          </button>
          <button type="button" class="btn-secondary" @click="repassword = null">Cancel</button>
        </div>
      </form>
    </AppDialog>

    <div class="space-y-2">
      <div class="flex items-center gap-3">
        <h3 class="subsection-title">API tokens</h3>
        <button type="button" class="btn-secondary" @click="creating = !creating">
          <Plus class="mr-1 size-4" aria-hidden="true" /> New token
        </button>
      </div>
      <p class="text-sm text-neutral-500">
        Sent as <span class="font-mono">Authorization: Bearer ost_…</span>.
      </p>

      <div
        v-if="minted"
        role="note"
        class="space-y-2 rounded-lg border border-amber-300 bg-amber-50 p-3 dark:border-amber-800 dark:bg-amber-950/40"
      >
        <p class="text-sm font-medium">
          Copy {{ minted.name }} now. It is not stored and cannot be shown again.
        </p>
        <code class="block font-mono text-code break-all select-all">{{ minted.secret }}</code>
        <button type="button" class="btn-secondary" @click="minted = null">Done</button>
      </div>

      <form v-if="creating" class="grid gap-3 sm:grid-cols-3" @submit.prevent="createToken">
        <FormField id="tok-name" label="Name">
          <input
            id="tok-name"
            v-model="form.name"
            class="input"
            required
            placeholder="monitoring"
          />
        </FormField>
        <FormField id="tok-role" label="Role">
          <select id="tok-role" v-model="form.role" class="input">
            <option v-for="r in ROLES" :key="r.value" :value="r.value">{{ r.label }}</option>
          </select>
        </FormField>
        <FormField id="tok-days" label="Expires after (days)" hint="0 never expires.">
          <div class="flex gap-2">
            <input
              id="tok-days"
              v-model.number="form.expiresInDays"
              type="number"
              min="0"
              max="3650"
              class="input w-24 font-mono"
            />
            <button type="submit" class="btn-primary">Create</button>
          </div>
        </FormField>
      </form>

      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Token</th>
              <th>Role</th>
              <th>Created</th>
              <th>Expires</th>
              <th>Last used</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="!tokens.length" key="empty" class="row-static">
              <td colspan="6" class="text-neutral-500">
                {{ load.updatedAt.value ? 'No API tokens.' : 'Reading tokens…' }}
              </td>
            </tr>
            <tr v-for="t in tokens" :key="t.id">
              <td>
                <div class="font-medium">{{ t.name }}</div>
                <div class="font-mono text-code text-neutral-500">{{ t.id }}</div>
              </td>
              <td class="font-mono text-code">{{ t.role }}</td>
              <td>{{ when(t.createdAt, '—') }}</td>
              <td>{{ when(t.expiresAt, 'never') }}</td>
              <td>{{ when(t.lastUsedAt, 'never') }}</td>
              <td class="text-right">
                <ConfirmButton
                  label="Delete"
                  :question="`Delete token ${t.name}?`"
                  description="Anything using it stops working."
                  :typed="t.name"
                  @confirm="deleteToken(t.id)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </div>
    </div>
  </section>
</template>
