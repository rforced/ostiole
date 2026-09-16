<script setup>
import { Plus } from 'lucide-vue-next'
import { onMounted, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'

const users = ref([])
const tokens = ref([])
const error = ref('')
const available = ref(true)
/** The one moment a new token's secret exists outside the server. */
const minted = ref(null)
const creating = ref(false)
const form = ref({ name: '', role: 'viewer', expiresInDays: 0 })

const ROLES = [
  { value: 'admin', label: 'Admin — everything, including accounts and updates' },
  { value: 'operator', label: 'Operator — change and apply the configuration' },
  { value: 'viewer', label: 'Viewer — read only, which is all a metrics scraper needs' },
]

onMounted(refresh)

async function refresh() {
  error.value = ''
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
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function setRole(username, role) {
  error.value = ''
  try {
    users.value = await api.users.setRole(username, role)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    await refresh()
  }
}

async function createToken() {
  error.value = ''
  try {
    minted.value = await api.tokens.create(form.value)
    form.value = { name: '', role: 'viewer', expiresInDays: 0 }
    creating.value = false
    await refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function deleteToken(id) {
  error.value = ''
  try {
    await api.tokens.remove(id)
    await refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

const when = (s, fallback) => (s ? new Date(s).toLocaleDateString() : fallback)
</script>

<template>
  <section v-if="available" class="card space-y-4" aria-labelledby="access-title">
    <h2 id="access-title" class="card-title">Accounts and API tokens</h2>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

    <div class="space-y-2">
      <h3 class="text-sm font-medium">Accounts</h3>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Account</th>
              <th>Role</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.username">
              <td class="font-mono">{{ u.username }}</td>
              <td>
                <select
                  class="input w-72"
                  :value="u.role"
                  :aria-label="`Role for ${u.username}`"
                  @change="setRole(u.username, $event.target.value)"
                >
                  <option v-for="r in ROLES" :key="r.value" :value="r.value">{{ r.label }}</option>
                </select>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="text-sm text-neutral-500">
        Accounts are created with <span class="font-mono">ostiole reset-password</span>, which is
        also how a forgotten password is fixed from the console.
      </p>
    </div>

    <div class="space-y-2">
      <div class="flex items-center gap-3">
        <h3 class="text-sm font-medium">API tokens</h3>
        <button type="button" class="btn-secondary" @click="creating = !creating">
          <Plus class="mr-1 size-4" aria-hidden="true" /> New token
        </button>
      </div>
      <p class="text-sm text-neutral-500">
        A token authenticates a script or a metrics scraper without a browser session. Send it as
        <span class="font-mono">Authorization: Bearer ost_…</span>. A viewer token is enough to
        scrape <span class="font-mono">/metrics</span>.
      </p>

      <div
        v-if="minted"
        role="note"
        class="space-y-2 rounded-lg border border-amber-300 bg-amber-50 p-3 dark:border-amber-800 dark:bg-amber-950/40"
      >
        <p class="text-sm font-medium">
          Copy {{ minted.name }} now. It is not stored and cannot be shown again.
        </p>
        <code class="block font-mono text-xs break-all select-all">{{ minted.secret }}</code>
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
          <tbody>
            <tr v-if="!tokens.length">
              <td colspan="6" class="text-neutral-500">No API tokens.</td>
            </tr>
            <tr v-for="t in tokens" :key="t.id">
              <td>
                <div class="font-medium">{{ t.name }}</div>
                <div class="font-mono text-xs text-neutral-500">{{ t.id }}</div>
              </td>
              <td class="font-mono text-xs">{{ t.role }}</td>
              <td class="text-xs">{{ when(t.createdAt, '—') }}</td>
              <td class="text-xs">{{ when(t.expiresAt, 'never') }}</td>
              <td class="text-xs">{{ when(t.lastUsedAt, 'never') }}</td>
              <td class="text-right">
                <ConfirmButton
                  label="Delete"
                  confirm-label="Delete? Anything using it stops working."
                  @confirm="deleteToken(t.id)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</template>
