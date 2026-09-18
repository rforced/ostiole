<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'

import ApplyPending from '@/components/ApplyPending.vue'
import FormField from '@/components/FormField.vue'
import { ApiError, api } from '@/lib/api'
import { useSystemStore } from '@/stores/system'

const CONFIRM_SECONDS = 90

const router = useRouter()
const system = useSystemStore()

const links = ref([])
const hostname = ref('')
const lan = ref('')
const lanAddress = ref('192.168.1.1/24')
const wan = ref('')
const managementFromWan = ref(false)
const services = ref(true)
const preview = ref(null)
const applied = ref(null)
const error = ref('')
const issues = ref([])
const busy = ref(false)
/** After the confirm: retiring the old firewall, and what went wrong if it did not. */
const finishing = ref(false)
const finishError = ref('')

const candidates = computed(() => links.value.filter((l) => l.kind !== 'loopback'))
const canPreview = computed(
  () => lan.value !== '' && lanAddress.value.trim() !== '' && lan.value !== wan.value,
)

onMounted(async () => {
  try {
    links.value = await api.interfaces.live()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
  // Sensible defaults: the interface holding the default route looks like WAN.
  const withAddr = candidates.value.filter((l) => l.addresses.length > 0)
  if (withAddr.length >= 2) {
    wan.value = withAddr[0].name
    lan.value = withAddr[1].name
  } else if (candidates.value.length >= 1) {
    lan.value = candidates.value[0].name
  }
})

function describe(l) {
  const addrs = l.addresses.length ? l.addresses.join(', ') : 'no address'
  return `${l.name} (${l.carrier ? 'up' : 'down'}${l.mac ? ', ' + l.mac : ''}, ${addrs})`
}

async function buildPreview() {
  error.value = ''
  issues.value = []
  busy.value = true
  try {
    preview.value = await api.config.starter({
      hostname: hostname.value.trim(),
      lan: lan.value,
      lanAddress: lanAddress.value.trim(),
      wan: wan.value,
      managementFromWan: wan.value !== '' && managementFromWan.value,
      services: services.value,
    })
  } catch (e) {
    preview.value = null
    if (e instanceof ApiError) {
      error.value = e.message
      issues.value = e.issues
    } else error.value = String(e)
  } finally {
    busy.value = false
  }
}

async function apply() {
  error.value = ''
  busy.value = true
  try {
    applied.value = await api.config.apply(preview.value, CONFIRM_SECONDS)
    await system.refresh()
  } catch (e) {
    if (e instanceof ApiError) {
      error.value = e.message
      issues.value = e.issues
    } else error.value = String(e)
  } finally {
    busy.value = false
  }
}

/**
 * The confirm is the first moment Ostiole's ruleset is in the kernel, so
 * it is the first moment the old firewall can safely be retired: that,
 * and clearing what it left behind, is done here rather than on a page
 * whose button would have refused until now. A daemon that is not root
 * cannot do it and is not asked.
 */
async function finish() {
  finishing.value = true
  finishError.value = ''
  if (system.host?.root) {
    try {
      await api.host.prepare()
    } catch (e) {
      finishError.value = e instanceof Error ? e.message : String(e)
    }
  }
  await system.refresh()
  await system.refreshHost()
  finishing.value = false
  if (!finishError.value) router.replace('/')
}

function reverted() {
  applied.value = null
}
</script>

<template>
  <div class="mx-auto max-w-2xl space-y-6">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Set up this firewall</h1>
      <p class="mt-1 text-sm text-neutral-500">
        Pick the interface facing your network and the one facing the internet.
      </p>
    </div>

    <section v-if="finishError" class="card space-y-3" aria-labelledby="finish-title">
      <h2 id="finish-title" class="card-title">Applied, but the old firewall is still running</h2>
      <p role="alert" class="text-sm text-red-600 dark:text-red-400">{{ finishError }}</p>
      <p class="text-sm text-neutral-500">
        Your configuration is in force. Retire the old firewall from
        <RouterLink to="/system/host" class="underline">System, Host</RouterLink> when you are
        ready.
      </p>
      <RouterLink to="/" class="btn-primary inline-block">Go to the dashboard</RouterLink>
    </section>
    <p v-else-if="finishing" role="status" class="text-sm text-neutral-500">
      Confirmed. Retiring the old firewall and clearing what it left behind.
    </p>
    <template v-else-if="applied">
      <ApplyPending :deadline="applied.deadline" @confirmed="finish" @reverted="reverted" />
    </template>

    <form v-else-if="!finishing" class="space-y-5" @submit.prevent="buildPreview">
      <FormField id="hostname" label="Hostname" hint="Optional.">
        <input
          id="hostname"
          v-model="hostname"
          class="input"
          autocomplete="off"
          spellcheck="false"
        />
      </FormField>

      <FormField
        id="lan"
        label="LAN interface"
        hint="Anti-lockout keeps the UI and SSH reachable from here."
      >
        <select id="lan" v-model="lan" class="input" required>
          <option value="" disabled>Choose an interface</option>
          <option v-for="l in candidates" :key="l.name" :value="l.name">{{ describe(l) }}</option>
        </select>
      </FormField>

      <FormField id="lan-address" label="LAN address" hint="e.g. 192.168.1.1/24">
        <input
          id="lan-address"
          v-model="lanAddress"
          class="input font-mono"
          required
          spellcheck="false"
        />
      </FormField>

      <FormField
        id="wan"
        label="WAN interface"
        hint="Optional. Takes its address by DHCP and NATs outbound traffic."
      >
        <select id="wan" v-model="wan" class="input">
          <option value="">None for now</option>
          <option v-for="l in candidates" :key="l.name" :value="l.name" :disabled="l.name === lan">
            {{ describe(l) }}
          </option>
        </select>
      </FormField>

      <label v-if="wan" class="flex items-start gap-2 text-sm">
        <input
          v-model="managementFromWan"
          type="checkbox"
          class="mt-0.5 size-4 rounded border-neutral-300"
        />
        <span>
          <span class="font-medium">Allow management from the WAN side too.</span>
          Adds a rule per management port on the wan zone, for a router administered over its public
          address. They are ordinary rules: narrow them to an address or delete them later under
          Firewall.
        </span>
      </label>

      <label class="flex items-start gap-2 text-sm">
        <input
          v-model="services"
          type="checkbox"
          class="mt-0.5 size-4 rounded border-neutral-300"
        />
        <span>
          <span class="font-medium">Run DHCP and DNS for the LAN.</span>
          Hands out addresses and resolves names for LAN clients. Needs
          <code class="font-mono">ostiole services setup</code> on the router first.
        </span>
      </label>

      <div v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">
        <p>{{ error }}</p>
        <ul v-if="issues.length" class="mt-1 list-disc pl-5">
          <li v-for="i in issues" :key="i.path">
            <span class="font-mono">{{ i.path }}</span
            >: {{ i.message }}
          </li>
        </ul>
      </div>

      <div class="flex items-center gap-3">
        <button type="submit" class="btn-secondary" :disabled="busy || !canPreview">Preview</button>
        <button v-if="preview" type="button" class="btn-primary" :disabled="busy" @click="apply">
          Apply with {{ CONFIRM_SECONDS }}s confirmation
        </button>
        <RouterLink to="/" class="ml-auto text-sm text-neutral-500 underline"
          >Skip for now</RouterLink
        >
      </div>
    </form>

    <section v-if="preview && !applied" class="card" aria-labelledby="preview-title">
      <h2 id="preview-title" class="card-title">What will be applied</h2>
      <ul class="list-disc space-y-1 pl-5">
        <li v-for="z in preview.zones" :key="z.name">
          Zone <span class="font-mono">{{ z.name }}</span
          >: {{ z.description }}<span v-if="z.external">, external (outbound NAT)</span
          ><span v-if="z.antiLockout">, anti-lockout on</span>
        </li>
        <li v-for="i in preview.interfaces" :key="i.name">
          <span class="font-mono">{{ i.name }}</span> in <span class="font-mono">{{ i.zone }}</span
          >: IPv4 {{ i.ipv4.mode }}<span v-if="i.ipv4.address"> {{ i.ipv4.address }}</span
          >, IPv6 {{ i.ipv6.mode }}
        </li>
        <li v-for="r in preview.rules" :key="r.id">
          Rule <span class="font-mono">{{ r.id }}</span
          >: {{ r.description }}
        </li>
        <li>
          Everything else inbound is dropped. Management ports
          {{ preview.system.management.webPort }} and {{ preview.system.management.sshPort }} stay
          open from the LAN.
        </li>
      </ul>
    </section>
  </div>
</template>
