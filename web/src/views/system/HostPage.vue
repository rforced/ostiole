<script setup>
import { computed, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'

import ConfirmButton from '@/components/ConfirmButton.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import { api } from '@/lib/api'
import { errorMessage, useAsync } from '@/lib/async'
import { SKIP_HOST_KEY } from '@/router'
import { useConfirmStore } from '@/stores/confirm'
import { useSystemStore } from '@/stores/system'

/**
 * Preparing the router Ostiole runs on. One button does the three steps
 * that can be done together, in the order they have to happen: the
 * packages the configuration needs, the old firewall (once a ruleset is
 * loaded), and whatever it left in the kernel. The addresses are separate,
 * because that is the step that can drop the session it is driven from.
 * Below that: how sshd lets people in, what the router has and does not
 * need, and the three steps in detail for anybody who wants them one at a
 * time.
 *
 * Everything here has a command-line twin (`ostiole host`), and the
 * actions are the same code: the daemon cannot write a unit file from
 * inside its sandbox, so the API runs the command an operator would have
 * typed and shows what it said.
 */

/** How long the revert timer waits for a confirmation. */
const NETWORK_WINDOW_S = 180

const confirm = useConfirmStore()
const router = useRouter()
const system = useSystemStore()
const report = ref(null)
const output = ref('')
const actionError = ref('')
/** Seconds left of a handover this page started, or 0. */
const countdown = ref(0)
let ticker = 0

const steps = computed(() => {
  const by = {}
  for (const s of report.value?.steps ?? []) by[s.step] = s
  return by
})
const components = computed(() => report.value?.components ?? [])
const competitors = computed(() => report.value?.competitors ?? [])
const legacy = computed(() => report.value?.legacy?.tables ?? [])
const network = computed(() => report.value?.network ?? {})
const extras = computed(() => report.value?.extras ?? [])
const ssh = computed(() => report.value?.ssh ?? {})
const accounts = computed(() => report.value?.accounts ?? [])
const root = computed(() => report.value?.root === true)
/** Whether Ostiole's own ruleset is in the kernel, which is what makes retiring the old one safe. */
const firewalled = computed(() => report.value?.firewalled === true)
/** A firewall that would be retired, if only a configuration had been applied yet. */
const firewallWaiting = computed(
  () => !firewalled.value && competitors.value.some((c) => c.kind === 'firewall' && c.conflicts),
)

/** The components this router needs and does not have. */
const outstanding = computed(() =>
  components.value.filter((c) => c.required && (!c.present || (c.unit && !c.ready))),
)
/** Leftovers a sweep would take: the ones nothing else is using. */
const sweepable = computed(() => legacy.value.filter((t) => !t.owner))

// Read straight from the API rather than through the store's refreshHost:
// that answers "prepared" for a router it cannot reach, which is right for
// the guard and wrong here, where a failed refresh should keep the last
// real report and show the error.
const refresh = useAsync(
  async () => {
    report.value = await api.host.status()
    system.host = report.value
  },
  { immediate: true },
)

const busy = ref(false)

/**
 * Run one action, keep its output, and take the report it answers with.
 *
 * @param {Promise<{output?: string, status?: object}>} call
 */
async function act(call) {
  busy.value = true
  actionError.value = ''
  output.value = ''
  try {
    const result = await call
    output.value = result.output ?? ''
    if (result.status) {
      report.value = result.status
      // The guard reads the store, so a step that has just been finished
      // stops sending the browser back here.
      system.host = result.status
    }
  } catch (e) {
    actionError.value = errorMessage(e)
    // The action failed, so what the router looks like now is worth
    // re-reading: half of it may have happened.
    await refresh.run()
  } finally {
    busy.value = false
  }
}

const setUp = (keys) => act(api.host.setup(keys))
const prepare = () => act(api.host.prepare())
const takeover = () => act(api.host.takeover())
const flush = (ids) => act(api.host.flushLegacy(ids))
const setSSH = (passwords) => act(api.host.ssh(passwords))

async function removeExtra(e) {
  busy.value = true
  actionError.value = ''
  try {
    output.value = (await api.host.removeExtras([e.key], true)).output ?? ''
  } catch (err) {
    actionError.value = errorMessage(err)
    return
  } finally {
    busy.value = false
  }
  const ok = await confirm.ask({
    question: e.maskOnly ? `Mask ${e.label}?` : `Remove ${e.label}?`,
    description: e.maskOnly
      ? 'Its units are stopped and masked. The package stays, because it is the package manager itself.'
      : 'Its units are stopped and masked, then the packages come off. Removing a package cannot be undone from here; what else comes away with it is above.',
    confirmLabel: e.maskOnly ? 'Mask' : 'Remove',
    typed: e.maskOnly ? '' : e.key,
  })
  if (ok) await act(api.host.removeExtras([e.key], false))
}

async function removePackages(unit, packages) {
  busy.value = true
  actionError.value = ''
  try {
    // The package manager is asked what it would do, and that is what
    // the operator agrees to: what else comes away with a package is its
    // answer to give, not Ostiole's.
    output.value = (await api.host.removePackages([unit], true)).output ?? ''
  } catch (e) {
    actionError.value = errorMessage(e)
    return
  } finally {
    busy.value = false
  }
  const ok = await confirm.ask({
    question: `Remove ${packages.join(', ')}?`,
    description:
      'Disabling and masking the unit has already stopped it. Removing the package cannot be undone from here; ' +
      'what else comes away with it is above.',
    confirmLabel: 'Remove',
    typed: unit,
  })
  if (ok) await act(api.host.removePackages([unit], false))
}

async function takeNetwork() {
  await act(api.host.network('take', `${NETWORK_WINDOW_S}s`))
  if (!actionError.value) startCountdown()
}

function startCountdown() {
  countdown.value = NETWORK_WINDOW_S
  clearInterval(ticker)
  ticker = setInterval(() => {
    countdown.value -= 1
    if (countdown.value <= 0) {
      clearInterval(ticker)
      countdown.value = 0
      refresh.run()
    }
  }, 1000)
}

async function confirmNetwork() {
  clearInterval(ticker)
  countdown.value = 0
  await act(api.host.network('confirm'))
}

async function revertNetwork() {
  clearInterval(ticker)
  countdown.value = 0
  await act(api.host.network('revert'))
}

const skip = (step, value) => act(api.host.skipStep(step, value))

/**
 * Let somebody past the gate for this browser session without recording
 * anything about the router: the steps are still outstanding, and the
 * page still says so.
 */
function continueAnyway() {
  sessionStorage.setItem(SKIP_HOST_KEY, '1')
  router.push('/')
}

onUnmounted(() => clearInterval(ticker))

/** The badge a step's state gets. */
function stepClass(state) {
  if (state === 'done') return 'badge badge-ok'
  if (state === 'skipped') return 'badge'
  return 'badge badge-warn'
}

/** What a component row says about itself, in one word. */
function componentState(c) {
  if (c.ready) return 'ready'
  if (c.present) return c.unit ? 'installed, no unit' : 'installed'
  if (c.availability === 'unpackaged') return 'not packaged here'
  if (c.availability === 'bundled') return 'missing'
  return 'not installed'
}

const stepTitles = {
  packages: 'Packages',
  firewall: 'Old firewall',
  legacy: 'Leftover rules',
  network: 'Addresses',
}
</script>

<template>
  <div class="space-y-6">
    <section class="card space-y-4" aria-labelledby="host-title">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="host-title" class="card-title">This router</h2>
          <p class="max-w-3xl text-sm text-neutral-500">
            Ostiole drives the software this distribution already packages. Everything here has a
            command-line twin under
            <code class="font-mono text-code">ostiole host</code>.
          </p>
        </div>
        <div class="flex items-center gap-3">
          <button
            v-if="report && !report.prepared"
            type="button"
            class="link"
            @click="continueAnyway"
          >
            Continue for now
          </button>
          <RefreshButton
            :busy="refresh.busy.value"
            :updated-at="refresh.updatedAt.value"
            @click="refresh.run"
          />
        </div>
      </div>

      <dl class="kv max-w-md">
        <dt>Distribution</dt>
        <dd>{{ report?.distro || 'unknown' }}</dd>
        <dt>Package manager</dt>
        <dd class="font-mono">{{ report?.manager || 'none found' }}</dd>
        <dt>Prepared</dt>
        <dd>{{ report?.prepared ? 'yes' : 'not yet' }}</dd>
      </dl>

      <p v-if="report && !root" class="text-sm text-amber-700 dark:text-amber-300">
        This daemon is not running as root, so none of these steps can be taken from here. It still
        reports what it found.
      </p>
      <p v-else-if="report?.unavailable" class="text-sm text-amber-700 dark:text-amber-300">
        {{ report.unavailable }}
      </p>

      <ul class="flex flex-wrap gap-2">
        <li v-for="(title, key) in stepTitles" :key="key" class="flex items-center gap-2">
          <span :class="stepClass(steps[key]?.state)">{{ title }}</span>
          <span v-if="steps[key]?.detail" class="text-sm text-neutral-500">
            {{ steps[key].detail }}
          </span>
        </li>
      </ul>

      <div
        v-if="root && report?.sentence"
        class="flex flex-wrap items-center gap-3 rounded-md border border-neutral-200 p-3 dark:border-neutral-800"
      >
        <p class="text-sm">
          <span class="font-medium">{{ report.sentence }}</span>
          <span v-if="firewallWaiting" class="block text-neutral-500">
            The old firewall is retired after your first apply; finishing the setup wizard does it.
          </span>
          <span class="block text-neutral-500">
            Addresses are handed over separately, below, because that step can drop this session.
          </span>
        </p>
        <button type="button" class="btn-primary ml-auto" :disabled="busy" @click="prepare">
          Prepare this router
        </button>
      </div>
      <p v-else-if="root && report && !firewallWaiting" class="text-sm text-neutral-500">
        Nothing to do: this router is prepared.
      </p>
      <p v-else-if="root && firewallWaiting" class="text-sm text-neutral-500">
        The old firewall is retired after your first apply; finishing the setup wizard does it.
      </p>
      <p v-if="busy" role="status" class="text-sm text-neutral-500">
        Working. Fetching packages can take a while.
      </p>

      <p v-if="actionError" role="alert" class="text-sm text-red-600 dark:text-red-400">
        {{ actionError }}
      </p>
      <p
        v-else-if="refresh.error.value"
        role="alert"
        class="text-sm text-red-600 dark:text-red-400"
      >
        {{ refresh.error.value }}
      </p>
      <pre
        v-if="output"
        class="max-h-64 overflow-auto rounded bg-neutral-50 p-2 font-mono text-code whitespace-pre-wrap text-neutral-700 dark:bg-neutral-900 dark:text-neutral-300"
        >{{ output }}</pre>
    </section>

    <!-- Access ----------------------------------------------------------- -->
    <section class="card space-y-4" aria-labelledby="host-access-title">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="host-access-title" class="card-title">SSH access</h2>
          <p class="max-w-3xl text-sm text-neutral-500">
            How sshd lets people in, read from the router each time. Requiring keys writes a drop-in
            that sorts before anything cloud-init leaves in
            <code class="font-mono text-code">sshd_config.d</code>, so it wins. Nothing checks that
            you have a key first: the accounts below say who does.
          </p>
        </div>
        <template v-if="root && ssh.present && !ssh.note">
          <button
            v-if="ssh.passwords"
            type="button"
            class="btn-primary"
            :disabled="busy"
            @click="setSSH(false)"
          >
            Require keys
          </button>
          <button v-else type="button" class="btn-secondary" :disabled="busy" @click="setSSH(true)">
            Allow passwords
          </button>
        </template>
      </div>

      <p v-if="!ssh.present" class="text-sm text-neutral-500">sshd is not on this router.</p>
      <p v-else-if="ssh.note" class="text-sm text-amber-700 dark:text-amber-300">{{ ssh.note }}</p>
      <dl v-else class="kv max-w-2xl">
        <dt>Password login</dt>
        <dd>
          <span :class="ssh.passwords ? 'badge badge-warn' : 'badge badge-ok'">
            {{ ssh.passwords ? 'allowed' : 'keys only' }}
          </span>
          <span v-if="ssh.setBy" class="ml-2 text-sm text-neutral-500">
            set by <span class="font-mono text-code">{{ ssh.setBy }}</span>
          </span>
        </dd>
        <dt>Root login</dt>
        <dd class="font-mono">{{ ssh.rootLogin || 'unknown' }}</dd>
        <dt>Managed by Ostiole</dt>
        <dd>{{ ssh.managed ? 'yes' : 'no' }}</dd>
      </dl>

      <div
        v-if="accounts.length"
        class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800"
      >
        <table class="table">
          <thead>
            <tr>
              <th>Account</th>
              <th>Can sudo</th>
              <th>Authorized keys</th>
              <th>Shell</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in accounts" :key="a.name">
              <td class="font-mono">{{ a.name }}</td>
              <td>{{ a.sudo ? 'yes' : 'no' }}</td>
              <td>
                <span :class="a.keys ? '' : 'text-amber-700 dark:text-amber-300'">{{
                  a.keys
                }}</span>
              </td>
              <td class="font-mono text-code">{{ a.shell }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Extras ----------------------------------------------------------- -->
    <section class="card space-y-4" aria-labelledby="host-extras-title">
      <div>
        <h2 id="host-extras-title" class="card-title">Not needed on a router</h2>
        <p class="max-w-3xl text-sm text-neutral-500">
          What the distribution image brought that a router has no use for: a second updater with a
          schedule of its own, and the daemons a desktop wants. Removing one stops and masks its
          units, then takes its packages off; what else comes away is the package manager's answer,
          shown before anything is removed.
        </p>
      </div>
      <p v-if="!extras.length" class="text-sm text-neutral-500">
        Nothing here that a router does not need.
      </p>
      <div
        v-else
        class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800"
      >
        <table class="table">
          <thead>
            <tr>
              <th>What</th>
              <th>Does</th>
              <th>State</th>
              <th>Packages</th>
              <th class="w-0"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in extras" :key="e.key">
              <td>
                <span class="font-medium">{{ e.label }}</span>
                <span class="badge ml-2">{{ e.kind }}</span>
              </td>
              <td class="text-sm text-neutral-500">{{ e.why }}</td>
              <td>
                <span v-if="e.removed" class="badge">removed, mask kept</span>
                <span
                  v-else
                  :class="e.active || e.enabled === 'enabled' ? 'badge badge-warn' : 'badge'"
                >
                  {{ e.active ? 'active' : e.enabled }}
                </span>
              </td>
              <td class="font-mono text-code">
                {{ (e.packages ?? []).join(' ') || (e.maskOnly ? 'mask only' : '—') }}
                <p v-if="e.note" class="font-sans text-sm text-neutral-500">{{ e.note }}</p>
              </td>
              <td>
                <button
                  v-if="root && !e.removed && (e.packages?.length || e.maskOnly)"
                  type="button"
                  class="btn-secondary"
                  :disabled="busy"
                  @click="removeExtra(e)"
                >
                  {{ e.maskOnly ? 'Mask' : 'Remove' }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <details class="group">
      <summary class="cursor-pointer text-sm font-medium text-neutral-600 dark:text-neutral-300">
        The three steps one at a time: packages, old firewall, leftover rules
      </summary>
      <div class="mt-4 space-y-6">
        <!-- 1. Packages ---------------------------------------------------- -->
        <section class="card space-y-4" aria-labelledby="host-pkgs-title">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 id="host-pkgs-title" class="card-title">Packages</h2>
              <p class="max-w-3xl text-sm text-neutral-500">
                Installing one writes Ostiole's unit for it as well. Nothing here starts a service:
                that is the switch on its own page, applied like every other change.
              </p>
            </div>
            <button
              v-if="root && outstanding.length"
              type="button"
              class="btn-primary"
              :disabled="busy"
              @click="setUp(outstanding.map((c) => c.key))"
            >
              Set up {{ outstanding.length }} missing
            </button>
          </div>

          <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
            <table class="table">
              <thead>
                <tr>
                  <th>Component</th>
                  <th>Needed for</th>
                  <th>State</th>
                  <th>Packages</th>
                  <th class="w-0"></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="c in components" :key="c.key">
                  <td>
                    <span class="font-medium">{{ c.label }}</span>
                    <span v-if="c.required" class="badge badge-warn ml-2">needed</span>
                    <p class="text-sm text-neutral-500">{{ c.needs }}</p>
                    <p v-if="c.note" class="mt-1 text-sm text-amber-700 dark:text-amber-300">
                      {{ c.note }}
                    </p>
                  </td>
                  <td class="text-sm text-neutral-500">
                    {{ c.why || 'nothing in this configuration' }}
                  </td>
                  <td>
                    <span :class="c.ready ? 'badge badge-ok' : 'badge badge-warn'">
                      {{ componentState(c) }}
                    </span>
                  </td>
                  <td class="font-mono text-code">{{ (c.packages ?? []).join(' ') || '—' }}</td>
                  <td>
                    <button
                      v-if="root && !c.ready && c.availability !== 'unpackaged'"
                      type="button"
                      class="btn-secondary"
                      :disabled="busy"
                      @click="setUp([c.key])"
                    >
                      Set up
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <button
            v-if="root && steps.packages?.state === 'outstanding'"
            type="button"
            class="link"
            @click="skip('packages', true)"
          >
            Leave this alone
          </button>
          <button
            v-else-if="root && steps.packages?.state === 'skipped'"
            type="button"
            class="link"
            @click="skip('packages', false)"
          >
            Ask about this again
          </button>
        </section>

        <!-- 2. Old firewall ------------------------------------------------ -->
        <section class="card space-y-4" aria-labelledby="host-fw-title">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 id="host-fw-title" class="card-title">Old firewall</h2>
              <p class="max-w-3xl text-sm text-neutral-500">
                Another firewall filters next to Ostiole's rules, and two sets of rules mean traffic
                has to pass both. Retiring one stops it and leaves a way back; removing its package
                does not. A retired unit stays masked after its package is removed, so a reinstall
                stays off.
              </p>
            </div>
            <button
              v-if="
                root && firewalled && competitors.some((c) => c.kind === 'firewall' && c.conflicts)
              "
              type="button"
              class="btn-primary"
              :disabled="busy"
              @click="takeover"
            >
              Retire competing firewalls
            </button>
          </div>
          <p v-if="firewallWaiting" class="text-sm text-neutral-500">
            Retired after your first apply, once Ostiole's own ruleset is in the kernel.
          </p>

          <p v-if="!competitors.length" class="text-sm text-neutral-500">
            Nothing else on this router does Ostiole's job.
          </p>
          <div
            v-else
            class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800"
          >
            <table class="table">
              <thead>
                <tr>
                  <th>Service</th>
                  <th>Does</th>
                  <th>State</th>
                  <th>Packages</th>
                  <th class="w-0"></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="c in competitors" :key="c.name">
                  <td class="font-mono">{{ c.name }}</td>
                  <td class="text-sm text-neutral-500">{{ c.kind }}</td>
                  <td>
                    <span v-if="c.removed" class="badge">removed, mask kept</span>
                    <span v-else :class="c.conflicts ? 'badge badge-warn' : 'badge badge-ok'">
                      {{ c.active }}, {{ c.enabled }}
                    </span>
                  </td>
                  <td class="font-mono text-code">
                    {{ (c.packages ?? []).join(' ') || '—' }}
                    <p v-if="c.note" class="font-sans text-sm text-neutral-500">{{ c.note }}</p>
                  </td>
                  <td>
                    <ConfirmButton
                      v-if="root && !c.conflicts && c.installed && c.packages?.length"
                      label="Remove packages"
                      :question="`Remove ${c.packages.join(', ')}?`"
                      description="What the package manager says it would take with them is shown before anything is removed."
                      confirm-label="Show me"
                      :danger="false"
                      @confirm="removePackages(c.name, c.packages)"
                    />
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <button
            v-if="root && steps.firewall?.state === 'outstanding'"
            type="button"
            class="link"
            @click="skip('firewall', true)"
          >
            Leave this alone
          </button>
          <button
            v-else-if="root && steps.firewall?.state === 'skipped'"
            type="button"
            class="link"
            @click="skip('firewall', false)"
          >
            Ask about this again
          </button>
        </section>

        <!-- 3. Leftover rules ---------------------------------------------- -->
        <section class="card space-y-4" aria-labelledby="host-legacy-title">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 id="host-legacy-title" class="card-title">Leftover rules</h2>
              <p class="max-w-3xl text-sm text-neutral-500">
                Rules an older firewall left in the kernel. They still filter traffic, and Ostiole
                never clears anybody else's rules on its own.
              </p>
            </div>
            <ConfirmButton
              v-if="root && sweepable.length"
              label="Clear leftovers"
              :question="`Clear ${sweepable.length} leftover ruleset${sweepable.length === 1 ? '' : 's'}?`"
              description="Legacy tables are emptied and their policies set to accept; nf_tables leftovers are deleted. Ostiole's own table is not touched."
              confirm-label="Clear"
              @confirm="flush([])"
            />
          </div>

          <p v-if="!legacy.length" class="text-sm text-neutral-500">
            Nothing was left behind.
            <span v-if="report?.legacy?.version" class="font-mono text-code">
              {{ report.legacy.version }}
            </span>
          </p>
          <div
            v-else
            class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800"
          >
            <table class="table">
              <thead>
                <tr>
                  <th>Ruleset</th>
                  <th>Rules</th>
                  <th>Chains</th>
                  <th class="w-0"></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="t in legacy" :key="t.backend + (t.family ?? '') + t.name">
                  <td class="font-mono">
                    {{ t.backend === 'legacy' ? 'legacy ' : '' }}{{ t.family }} {{ t.name }}
                  </td>
                  <td>{{ t.rules || 0 }}</td>
                  <td class="font-mono text-code">
                    {{ (t.chains ?? []).join(' ') || '—' }}
                    <p v-if="t.owner" class="font-sans text-sm text-neutral-500">
                      Belongs to {{ t.owner }}, so it is left alone.
                    </p>
                  </td>
                  <td>
                    <ConfirmButton
                      v-if="root && t.owner"
                      label="Clear anyway"
                      :question="`Clear ${t.family} ${t.name}?`"
                      :description="`This ruleset belongs to ${t.owner}. Clearing it breaks whatever is using it until that is restarted.`"
                      confirm-label="Clear"
                      :typed="t.name"
                      @confirm="
                        flush([(t.backend === 'legacy' ? 'legacy ' : '') + t.family + ' ' + t.name])
                      "
                    />
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <button
            v-if="root && steps.legacy?.state === 'outstanding'"
            type="button"
            class="link"
            @click="skip('legacy', true)"
          >
            Leave this alone
          </button>
          <button
            v-else-if="root && steps.legacy?.state === 'skipped'"
            type="button"
            class="link"
            @click="skip('legacy', false)"
          >
            Ask about this again
          </button>
        </section>
      </div>
    </details>

    <!-- 4. Addresses --------------------------------------------------- -->
    <section class="card space-y-4" aria-labelledby="host-net-title">
      <h2 id="host-net-title" class="card-title">Addresses</h2>
      <p class="max-w-3xl text-sm text-neutral-500">
        Ostiole configures this router's addresses and routes through systemd-networkd. Handing them
        over stops the manager that has them now, which can drop this session, so it runs in a unit
        of its own and puts the old manager back unless it is confirmed.
      </p>

      <dl class="kv max-w-md">
        <dt>systemd-networkd</dt>
        <dd>{{ network.networkd }}</dd>
        <dt>Backend</dt>
        <dd class="font-mono">{{ network.backend }}</dd>
        <dt>Owned by Ostiole</dt>
        <dd>{{ network.owned ? 'yes' : 'no' }}</dd>
        <dt v-if="network.managers?.length">{{ network.owned ? 'Retired' : 'In charge' }}</dt>
        <dd v-if="network.managers?.length" class="font-mono">
          {{ network.managers.join(', ') }}
        </dd>
      </dl>

      <div
        v-if="network.pending || countdown"
        role="note"
        class="rounded-md border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-900 dark:bg-amber-950/40"
      >
        <p class="font-medium text-amber-800 dark:text-amber-300">
          A handover is waiting to be confirmed<span v-if="countdown">
            — {{ countdown }}s left</span
          >
        </p>
        <p class="text-amber-800/80 dark:text-amber-300/80">
          If this page cannot reach the router, do nothing: the previous manager comes back on its
          own. Confirm only once you have loaded a page over the new addressing.
        </p>
        <div class="mt-2 flex flex-wrap gap-2">
          <button type="button" class="btn-primary" :disabled="busy" @click="confirmNetwork">
            Keep it
          </button>
          <button type="button" class="btn-secondary" :disabled="busy" @click="revertNetwork">
            Put the old manager back
          </button>
        </div>
      </div>
      <div v-else-if="root" class="flex flex-wrap items-center gap-3">
        <ConfirmButton
          v-if="!network.owned && network.backend !== 'none'"
          label="Hand addressing over"
          question="Hand this router's addresses to systemd-networkd?"
          :description="`The manager in charge now is stopped and masked. If this session drops, the switch still finishes, and the old manager comes back in ${NETWORK_WINDOW_S} seconds unless you confirm.`"
          confirm-label="Hand over"
          @confirm="takeNetwork"
        />
        <ConfirmButton
          v-if="network.owned"
          label="Give it back"
          question="Put the previous network manager back?"
          description="systemd-networkd is stopped and the manager it replaced is restored. Ostiole stops configuring addresses."
          confirm-label="Give it back"
          @confirm="revertNetwork"
        />
      </div>
      <button
        v-if="root && steps.network?.state === 'outstanding'"
        type="button"
        class="link"
        @click="skip('network', true)"
      >
        This router keeps its own network manager
      </button>
      <button
        v-else-if="root && steps.network?.state === 'skipped'"
        type="button"
        class="link"
        @click="skip('network', false)"
      >
        Ask about this again
      </button>
    </section>
  </div>
</template>
