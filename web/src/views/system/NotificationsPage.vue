<script setup>
import { LoaderCircle, Send } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { parseList } from '@/lib/lists'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

const REFRESH_MS = 15_000

const auth = useAuthStore()
const config = useConfigStore()
const settings = computed(() => config.notifications)
const email = computed(() => settings.value.email ?? {})
const webhook = computed(() => settings.value.webhook ?? {})
// Where the router's news goes is an admin's to say, as the apply enforces.
const locked = computed(() => !auth.isAdmin)

/**
 * @param {object} patch
 * @param {'email' | 'webhook'} [part]
 */
const set = (patch, part) => config.setNotifications(patch, part)

/**
 * What a notice can be about, grouped the way the sidebar is. The values
 * are the dashboard's warning kinds and the ones only a notice has
 * (internal/notify).
 */
const KINDS = [
  {
    group: 'Firewall',
    kinds: [
      { value: 'fallback-ruleset', label: 'The fallback ruleset is loaded' },
      { value: 'table-missing', label: 'No ruleset is loaded' },
      { value: 'apply-reverted', label: 'An apply was not confirmed in time' },
      { value: 'apply-undone', label: 'An apply was undone at start' },
      { value: 'foreign-tables', label: 'Other rulesets are loaded' },
    ],
  },
  {
    group: 'Network',
    kinds: [
      { value: 'gateway-down', label: 'A gateway stops answering' },
      { value: 'interface-missing', label: 'An interface goes missing' },
      { value: 'forwarding-off', label: 'IP forwarding is off' },
    ],
  },
  {
    group: 'Services',
    kinds: [
      { value: 'service-down', label: 'A service stops running' },
      { value: 'conflicting-services', label: 'A conflicting service runs' },
      { value: 'tc-missing', label: 'Traffic shaping cannot run' },
      { value: 'feed-stale', label: 'A list stops refreshing' },
      { value: 'waf', label: 'What the web application firewall blocked, daily' },
    ],
  },
  {
    group: 'System',
    kinds: [
      { value: 'drive-failing', label: 'A drive reports it is failing' },
      { value: 'certificate', label: 'A certificate is not renewing' },
      { value: 'remote-backup-failed', label: 'A remote backup fails' },
      { value: 'kernel-unsupported', label: 'The kernel is too old' },
      { value: 'update-available', label: 'A new Ostiole release' },
      { value: 'os-updates', label: 'Package updates need attention' },
    ],
  },
]

const muted = (kind) => (settings.value.mute ?? []).includes(kind)

function setKind(kind, on) {
  const mute = new Set(settings.value.mute ?? [])
  if (on) mute.delete(kind)
  else mute.add(kind)
  set({ mute: [...mute].sort() })
}

const recipients = computed({
  get: () => (email.value.to ?? []).join(', '),
  set: (v) => set({ to: parseList(v) }, 'email'),
})

const status = ref({ targets: {}, recent: [] })
const load = useAsync(
  async () => {
    status.value = await api.notifications.status()
  },
  { interval: REFRESH_MS },
)
onMounted(load.run)

const TARGETS = { email: 'Mail', webhook: 'Webhook' }
/** What each target did with the last test, in the order the form has them. */
const results = ref([])
// The draft, not what is applied: the point is to find out before.
const testing = useAsync(async () => {
  results.value = []
  results.value = await api.notifications.test(config.draft?.notifications ?? {})
  await load.run()
})
const canTest = computed(() => auth.isAdmin && (email.value.enabled || webhook.value.enabled))

const when = (s) => (s ? new Date(s).toLocaleString() : 'never')
const headline = (e) => (e.level === 'ok' ? `Resolved: ${e.title}` : e.title)
const targetStates = computed(() =>
  Object.entries(TARGETS)
    .map(([key, label]) => ({ key, label, ...(status.value.targets?.[key] ?? {}) }))
    .filter((t) => t.lastTried),
)
</script>

<template>
  <div class="space-y-5">
    <SectionCard
      title="Notifications"
      intro="What the dashboard warns about, by mail or to a webhook. Off, nothing leaves this router."
    >
      <template #actions>
        <ToggleRow
          id="nt-enabled"
          :model-value="settings.enabled === true"
          variant="switch"
          label="Enabled"
          aria-label="Notifications enabled"
          :disabled="locked"
          @update:model-value="set({ enabled: $event })"
        />
      </template>

      <div class="space-y-4">
        <p v-if="auth.isOperator" class="text-ink-muted">Only an admin can change these.</p>

        <fieldset class="min-w-0 space-y-4" :disabled="locked">
          <legend class="group-title mb-1">Mail</legend>
          <ToggleRow
            id="nt-email"
            :model-value="email.enabled === true"
            label="Send mail"
            @update:model-value="set({ enabled: $event }, 'email')"
          />
          <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
            <FormField
              id="nt-server"
              label="Mail server"
              hint="The port follows the security unless you give one."
            >
              <input
                id="nt-server"
                class="input font-mono"
                placeholder="smtp.example.net"
                spellcheck="false"
                :value="email.server ?? ''"
                @change="set({ server: $event.target.value.trim() }, 'email')"
              />
            </FormField>
            <FormField id="nt-security" label="Security">
              <select
                id="nt-security"
                class="input"
                :value="email.security || 'starttls'"
                @change="
                  set(
                    { security: $event.target.value === 'starttls' ? '' : $event.target.value },
                    'email',
                  )
                "
              >
                <option value="starttls">STARTTLS, port 587</option>
                <option value="tls">TLS, port 465</option>
                <option value="none">None, port 25</option>
              </select>
            </FormField>
            <FormField id="nt-user" label="User name" hint="Empty sends without logging in.">
              <input
                id="nt-user"
                class="input"
                autocomplete="off"
                :value="email.username ?? ''"
                @change="set({ username: $event.target.value.trim() }, 'email')"
              />
            </FormField>
            <FormField id="nt-password" label="Password">
              <input
                id="nt-password"
                type="password"
                class="input"
                autocomplete="new-password"
                :value="email.password ?? ''"
                @change="set({ password: $event.target.value }, 'email')"
              />
            </FormField>
            <FormField id="nt-from" label="From">
              <input
                id="nt-from"
                class="input"
                placeholder="Router <router@example.net>"
                :value="email.from ?? ''"
                @change="set({ from: $event.target.value.trim() }, 'email')"
              />
            </FormField>
            <FormField id="nt-to" label="To" hint="Comma separated.">
              <input id="nt-to" v-model.lazy="recipients" class="input" />
            </FormField>
          </div>
        </fieldset>

        <fieldset class="min-w-0 space-y-4" :disabled="locked">
          <legend class="group-title mb-1">Webhook</legend>
          <ToggleRow
            id="nt-webhook"
            :model-value="webhook.enabled === true"
            label="Post to a webhook"
            @update:model-value="set({ enabled: $event }, 'webhook')"
          />
          <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
            <FormField id="nt-url" label="URL" hint="https only.">
              <input
                id="nt-url"
                class="input font-mono"
                spellcheck="false"
                autocomplete="off"
                :value="webhook.url ?? ''"
                @change="set({ url: $event.target.value.trim() }, 'webhook')"
              />
            </FormField>
            <FormField id="nt-format" label="Format">
              <select
                id="nt-format"
                class="input"
                :value="webhook.format || 'json'"
                @change="
                  set(
                    { format: $event.target.value === 'json' ? '' : $event.target.value },
                    'webhook',
                  )
                "
              >
                <option value="json">JSON</option>
                <option value="slack">Slack, Mattermost or Google Chat</option>
                <option value="discord">Discord</option>
                <option value="ntfy">ntfy</option>
              </select>
            </FormField>
            <FormField id="nt-token" label="Token" hint="Sent as a bearer token. Empty sends none.">
              <input
                id="nt-token"
                type="password"
                class="input"
                autocomplete="new-password"
                :value="webhook.token ?? ''"
                @change="set({ token: $event.target.value.trim() }, 'webhook')"
              />
            </FormField>
          </div>
        </fieldset>
      </div>
    </SectionCard>

    <SectionCard title="What to send">
      <fieldset class="grid min-w-0 gap-x-8 gap-y-5 sm:grid-cols-2" :disabled="locked">
        <div v-for="g in KINDS" :key="g.group" class="space-y-1.5">
          <p class="group-title">{{ g.group }}</p>
          <ToggleRow
            v-for="k in g.kinds"
            :key="k.value"
            :label="k.label"
            :model-value="!muted(k.value)"
            @update:model-value="setKind(k.value, $event)"
          />
        </div>
      </fieldset>
      <p class="mt-4 text-ink-muted">
        A warning goes out once it has held for a minute, and again when it ends.
      </p>
    </SectionCard>

    <SectionCard title="Sent" :count="status.recent.length" flush>
      <template #actions>
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run"
        />
        <button
          v-if="!auth.readOnly"
          type="button"
          class="btn-secondary"
          :disabled="!canTest || testing.busy.value"
          :aria-busy="testing.busy.value"
          @click="testing.run"
        >
          <LoaderCircle v-if="testing.busy.value" class="size-4 animate-spin" aria-hidden="true" />
          <Send v-else class="size-4" aria-hidden="true" />
          {{ testing.busy.value ? 'Sending…' : 'Send a test' }}
        </button>
      </template>
      <div v-if="testing.error.value || results.length" class="card-strip space-y-1">
        <p v-if="testing.error.value" role="alert" class="text-bad">{{ testing.error.value }}</p>
        <p v-for="r in results" :key="r.target" :class="r.error ? 'text-bad' : 'text-ok'">
          {{ TARGETS[r.target] }}: {{ r.error || 'sent. Check that it arrived.' }}
        </p>
      </div>
      <div v-if="targetStates.length" class="card-strip">
        <dl class="kv">
          <template v-for="t in targetStates" :key="t.key">
            <dt>{{ t.label }}</dt>
            <dd>
              last sent {{ when(t.lastSent) }}
              <span v-if="t.lastError" class="block text-bad">{{ t.lastError }}</span>
            </dd>
          </template>
        </dl>
      </div>
      <table class="table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Notices</th>
            <th>Went to</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!status.recent.length">
            <td colspan="3" class="text-ink-muted">
              {{ load.updatedAt.value ? 'Nothing sent.' : 'Reading…' }}
            </td>
          </tr>
          <tr v-for="r in status.recent" :key="r.time">
            <td class="text-xs whitespace-nowrap">{{ when(r.time) }}</td>
            <td>
              <div v-for="(e, i) in r.events" :key="i">{{ headline(e) }}</div>
              <div v-if="r.dropped" class="text-ink-muted">and {{ r.dropped }} dropped</div>
            </td>
            <td>
              {{ r.sent.map((s) => TARGETS[s] ?? s).join(', ') || '—' }}
              <div v-for="(err, target) in r.failed ?? {}" :key="target" class="text-bad">
                {{ TARGETS[target] ?? target }}: {{ err }}
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>
  </div>
</template>
