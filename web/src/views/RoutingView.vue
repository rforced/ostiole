<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import { api } from '@/lib/api'
import { newId } from '@/lib/ids'
import { useConfigStore } from '@/stores/config'
import GatewayDialog from '@/views/routing/GatewayDialog.vue'
import GatewayGroupDialog from '@/views/routing/GatewayGroupDialog.vue'

const config = useConfigStore()
const open = ref(false)
const editing = ref(null)
const form = ref(blank())
const gwOpen = ref(false)
const gwEditing = ref(null)
const groupOpen = ref(false)
const groupEditing = ref(null)
const live = ref([])
const policy = ref([])
let timer = null

/** Configured gateways merged with what the monitor sees. */
const gatewayRows = computed(() =>
  config.gateways.map((g) => ({ ...g, live: live.value.find((l) => l.name === g.name) ?? null })),
)

/** Groups merged with where the kernel currently sends their traffic. */
const groupRows = computed(() =>
  config.gatewayGroups.map((g) => ({
    ...g,
    policy: policy.value.find((p) => p.name === g.name) ?? null,
  })),
)

/** Rules that pick a gateway, which is what policy routing exists for. */
const policyRules = computed(() => config.rules.filter((r) => r.gateway))

async function refreshGateways() {
  try {
    live.value = await api.gateways()
  } catch {
    live.value = []
  }
  try {
    policy.value = await api.policy()
  } catch {
    policy.value = []
  }
}

function addGateway() {
  gwEditing.value = null
  gwOpen.value = true
}
function editGateway(g) {
  gwEditing.value = g
  gwOpen.value = true
}
function addGroup() {
  groupEditing.value = null
  groupOpen.value = true
}
function editGroup(g) {
  groupEditing.value = g
  groupOpen.value = true
}

/** Summarises a group's members as the tiers they fail over through. */
function tierSummary(group) {
  const tiers = new Map()
  for (const m of group.members ?? []) {
    const tier = m.tier ?? 0
    tiers.set(tier, [...(tiers.get(tier) ?? []), m.gateway])
  }
  return [...tiers.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([, names]) => names.join(' + '))
    .join(' → ')
}

function blank() {
  return { id: '', description: '', enabled: true, destination: '', gateway: '', interface: '' }
}

onMounted(async () => {
  await config.load()
  await refreshGateways()
  timer = setInterval(refreshGateways, 10_000)
})
onUnmounted(() => clearInterval(timer))

watch(
  () => [open.value, editing.value],
  () => {
    if (open.value) form.value = editing.value ? { ...blank(), ...editing.value } : blank()
  },
)

function add() {
  editing.value = null
  open.value = true
}
function edit(r) {
  editing.value = r
  open.value = true
}
function save() {
  const f = form.value
  const out = {
    id: f.id || newId('rt'),
    enabled: f.enabled,
    destination: f.destination.trim(),
    gateway: f.gateway.trim(),
  }
  if (f.description) out.description = f.description
  if (f.interface) out.interface = f.interface
  config.upsertRoute(out)
  open.value = false
}
</script>

<template>
  <div class="space-y-4">
    <h1 class="text-2xl font-semibold tracking-tight">Routing</h1>
    <p v-if="config.error" role="alert" class="text-sm text-red-600 dark:text-red-400">
      {{ config.error }}
    </p>
    <p v-if="config.loaded && !config.draft" class="text-sm text-neutral-500">
      No configuration yet.
      <RouterLink to="/wizard" class="underline">Run the setup wizard</RouterLink> first.
    </p>
    <template v-else-if="config.draft">
      <section class="space-y-3" aria-labelledby="gw-title">
        <div class="flex items-center gap-3">
          <h2 id="gw-title" class="font-medium">Gateways</h2>
          <button type="button" class="btn-secondary" @click="addGateway">
            <Plus class="mr-1 size-4" aria-hidden="true" /> Add gateway
          </button>
        </div>
        <p class="max-w-3xl text-sm text-neutral-500">
          List every upstream here to get failover: the firewall probes each one and moves the
          default route off a gateway that stops answering. With no gateways listed, the address
          from the interface is used as it is.
        </p>
        <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
          <table class="table">
            <thead>
              <tr>
                <th>Gateway</th>
                <th>Interface</th>
                <th>Address</th>
                <th>Monitor</th>
                <th>Priority</th>
                <th>State</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!gatewayRows.length">
                <td colspan="7" class="text-neutral-500">
                  No gateways. One per WAN gives you failover.
                </td>
              </tr>
              <tr v-for="g in gatewayRows" :key="g.name" :class="{ 'opacity-50': !g.enabled }">
                <td>
                  <div class="font-mono font-medium">{{ g.name }}</div>
                  <div class="text-xs text-neutral-500">{{ g.description }}</div>
                </td>
                <td class="font-mono text-xs">{{ g.interface }}</td>
                <td class="font-mono text-xs">{{ g.live?.address || g.address || 'from DHCP' }}</td>
                <td class="font-mono text-xs">{{ g.monitor || 'the gateway' }}</td>
                <td class="font-mono text-xs">{{ g.priority ?? 0 }}</td>
                <td class="text-xs whitespace-nowrap">
                  <template v-if="g.live && !g.live.unknown">
                    <span class="badge" :class="g.live.online ? 'badge-ok' : 'badge-warn'">
                      {{ g.live.online ? 'up' : 'down' }}
                    </span>
                    <span v-if="g.live.active" class="badge badge-ok ml-1">active</span>
                    <div class="mt-1 font-mono text-neutral-500">
                      {{ g.live.latencyMs.toFixed(1) }}ms · {{ g.live.lossPercent.toFixed(0) }}%
                      loss
                    </div>
                  </template>
                  <span v-else class="badge">not probed</span>
                </td>
                <td class="text-right whitespace-nowrap">
                  <button type="button" class="link" @click="editGateway(g)">Edit</button>
                  <ConfirmButton
                    class="ml-3"
                    label="Delete"
                    :confirm-label="
                      config.gatewayReferences(g.name).length
                        ? `Used by ${config.gatewayReferences(g.name).join(', ')}. Delete anyway?`
                        : 'Delete gateway?'
                    "
                    @confirm="config.removeGateway(g.name)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="space-y-3" aria-labelledby="gg-title">
        <div class="flex items-center gap-3">
          <h2 id="gg-title" class="font-medium">Gateway groups</h2>
          <button
            type="button"
            class="btn-secondary"
            :disabled="!config.gateways.length"
            @click="addGroup"
          >
            <Plus class="mr-1 size-4" aria-hidden="true" /> Add group
          </button>
        </div>
        <p class="max-w-3xl text-sm text-neutral-500">
          A group is what a firewall rule points at when it should not follow the default route: the
          lowest tier that is up carries the traffic, and gateways in the same tier share it.
        </p>
        <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
          <table class="table">
            <thead>
              <tr>
                <th>Group</th>
                <th>Members</th>
                <th>When all are down</th>
                <th>Rules</th>
                <th>Next hop</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!groupRows.length">
                <td colspan="6" class="text-neutral-500">
                  <template v-if="config.gateways.length">
                    No groups. Add one to route some rules over a different line.
                  </template>
                  <template v-else> Add a gateway first; a group is made of them. </template>
                </td>
              </tr>
              <tr v-for="g in groupRows" :key="g.name" :class="{ 'opacity-50': !g.enabled }">
                <td>
                  <div class="font-mono font-medium">{{ g.name }}</div>
                  <div class="text-xs text-neutral-500">{{ g.description }}</div>
                </td>
                <td class="font-mono text-xs">{{ tierSummary(g) || '—' }}</td>
                <td class="text-xs">
                  <span v-if="g.onDown === 'block'" class="badge badge-warn">drop</span>
                  <span v-else class="text-neutral-500">default route</span>
                </td>
                <td class="font-mono text-xs">{{ g.policy?.rules ?? 0 }}</td>
                <td class="font-mono text-xs">
                  <template v-if="g.policy?.nextHops?.length">
                    {{ g.policy.nextHops.join(', ') }}
                  </template>
                  <span v-else-if="g.onDown === 'block'" class="badge badge-warn"
                    >blocking traffic</span
                  >
                  <span v-else class="text-neutral-500">default route</span>
                </td>
                <td class="text-right whitespace-nowrap">
                  <button type="button" class="link" @click="editGroup(g)">Edit</button>
                  <ConfirmButton
                    class="ml-3"
                    label="Delete"
                    :confirm-label="
                      config.gatewayReferences(g.name).length
                        ? `Used by ${config.gatewayReferences(g.name).join(', ')}. Delete anyway?`
                        : 'Delete group?'
                    "
                    @confirm="config.removeGatewayGroup(g.name)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="policyRules.length" class="text-sm text-neutral-500">
          <span class="font-medium">Policy routing is in use:</span>
          <template v-for="(r, i) in policyRules" :key="r.id">
            <span v-if="i">,</span>
            <RouterLink to="/firewall" class="underline"> {{ r.id }}</RouterLink>
            → {{ r.gateway }}
          </template>
        </p>
      </section>

      <h2 class="font-medium">Static routes</h2>
      <button type="button" class="btn-secondary" @click="add">
        <Plus class="mr-1 size-4" aria-hidden="true" /> Add static route
      </button>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Destination</th>
              <th>Gateway</th>
              <th>Interface</th>
              <th>Description</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="config.routes.length === 0">
              <td colspan="5" class="text-neutral-500">No static routes.</td>
            </tr>
            <tr v-for="r in config.routes" :key="r.id" :class="{ 'opacity-50': !r.enabled }">
              <td class="font-mono text-xs">{{ r.destination }}</td>
              <td class="font-mono text-xs">{{ r.gateway }}</td>
              <td class="font-mono text-xs">{{ r.interface ?? 'auto' }}</td>
              <td>{{ r.description }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="edit(r)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  confirm-label="Delete route?"
                  @confirm="config.removeRoute(r.id)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <GatewayDialog v-model:open="gwOpen" :gateway="gwEditing" />
    <GatewayGroupDialog v-model:open="groupOpen" :group="groupEditing" />

    <AppDialog v-model:open="open" :title="editing ? `Route ${editing.id}` : 'New static route'">
      <form class="space-y-4" @submit.prevent="save">
        <FormField id="rt-desc" label="Description">
          <input id="rt-desc" v-model="form.description" class="input" />
        </FormField>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField id="rt-dest" label="Destination network">
            <input
              id="rt-dest"
              v-model="form.destination"
              class="input font-mono"
              placeholder="10.200.0.0/16"
              required
              spellcheck="false"
            />
          </FormField>
          <FormField id="rt-gw" label="Gateway">
            <input
              id="rt-gw"
              v-model="form.gateway"
              class="input font-mono"
              placeholder="10.10.0.254"
              required
              spellcheck="false"
            />
          </FormField>
        </div>
        <FormField
          id="rt-if"
          label="Interface"
          hint="Optional; otherwise chosen from the gateway's network."
        >
          <select id="rt-if" v-model="form.interface" class="input">
            <option value="">Automatic</option>
            <option v-for="i in config.interfaces" :key="i.name" :value="i.name">
              {{ i.name }}
            </option>
          </select>
        </FormField>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
          Enabled
        </label>
        <div class="flex justify-end gap-2 pt-2">
          <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
          <button type="submit" class="btn-primary">Save to draft</button>
        </div>
      </form>
    </AppDialog>
  </div>
</template>
