<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'

import ApplyPending from '@/components/ApplyPending.vue'
import FormField from '@/components/FormField.vue'
import PageHeader from '@/components/PageHeader.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { ApiError, api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import { useSystemStore } from '@/stores/system'

const CONFIRM_SECONDS = 90

const router = useRouter()
const config = useConfigStore()
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
/** The request in flight: 'preview', 'apply' or ''. */
const busy = ref('')
/** Set once the apply is confirmed, while the status is re-read. */
const finishing = ref(false)

const candidates = computed(() => links.value.filter((l) => l.kind !== 'loopback' && !l.wireless))
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
  busy.value = 'preview'
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
    busy.value = ''
  }
}

async function apply() {
  error.value = ''
  busy.value = 'apply'
  try {
    applied.value = await api.config.apply(preview.value, CONFIRM_SECONDS)
    await system.refresh()
  } catch (e) {
    if (e instanceof ApiError) {
      error.value = e.message
      issues.value = e.issues
    } else error.value = String(e)
  } finally {
    busy.value = ''
  }
}

/** The apply is confirmed and in force; the dashboard takes it from here. */
async function finish() {
  finishing.value = true
  // The configuration the wizard applied never went through the draft.
  await Promise.all([system.refresh(), config.resync()])
  finishing.value = false
  router.replace('/')
}

function reverted() {
  applied.value = null
}
</script>

<template>
  <div class="mx-auto max-w-2xl space-y-5">
    <PageHeader
      title="Set up this firewall"
      intro="Pick the interface facing your network and the one facing the internet."
    />

    <p v-if="finishing" role="status" class="text-sm text-ink-muted">Confirmed.</p>
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
          <option value="" disabled>Choose</option>
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

      <ToggleRow
        v-if="wan"
        v-model="managementFromWan"
        label="Allow management from the WAN side too"
        hint="Turns anti-lockout on for the wan zone, for a router administered over its public
          address. Untick it on the zone under Interfaces to take it away."
      />

      <ToggleRow
        v-model="services"
        label="Run DHCP and DNS for the LAN"
        hint="Hands out addresses and resolves names for LAN clients."
      />

      <div v-if="error" role="alert" class="text-sm text-bad">
        <p>{{ error }}</p>
        <ul v-if="issues.length" class="mt-1 list-disc pl-5">
          <li v-for="i in issues" :key="i.path">
            <span class="font-mono">{{ i.path }}</span
            >: {{ i.message }}
          </li>
        </ul>
      </div>

      <div class="flex items-center gap-3">
        <button
          type="submit"
          class="btn-secondary"
          :disabled="busy !== '' || !canPreview"
          :aria-busy="busy === 'preview'"
        >
          <LoaderCircle v-if="busy === 'preview'" class="size-4 animate-spin" aria-hidden="true" />
          Preview
        </button>
        <button
          v-if="preview"
          type="button"
          class="btn-primary"
          :disabled="busy !== ''"
          :aria-busy="busy === 'apply'"
          @click="apply"
        >
          <LoaderCircle v-if="busy === 'apply'" class="size-4 animate-spin" aria-hidden="true" />
          {{ busy === 'apply' ? 'Applying…' : `Apply with ${CONFIRM_SECONDS}s confirmation` }}
        </button>
        <RouterLink to="/" class="ml-auto text-sm text-ink-muted underline"
          >Skip for now</RouterLink
        >
      </div>
    </form>

    <SectionCard v-if="preview && !applied" title="What will be applied">
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
    </SectionCard>
  </div>
</template>
