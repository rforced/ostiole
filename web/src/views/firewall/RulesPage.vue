<script setup>
import { ArrowDown, ArrowUp, Plus } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { useDraftRows } from '@/lib/draft'
import { useTabHash } from '@/lib/tabs'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import RuleDialog from '@/views/firewall/RuleDialog.vue'
import SystemRuleRow from '@/views/firewall/SystemRuleRow.vue'
import { tierBadge, tierLabel } from '@/views/firewall/shaping/tiers'

/** Where each system rule is controlled from, by the setting the server names. */
const SETTINGS = {
  zone: '/interfaces#zones',
  interface: '/interfaces',
  dhcp: '/services/dhcp',
  shaping: '/firewall/shaping',
  dns: '/services/dns',
  enforcement: '/services/dns#enforcement',
  queryLog: '/services/dns#queries',
  upnp: '/services/upnp',
  proxy: '/services/proxy',
  wireguard: '/vpn/wireguard',
  tailscale: '/vpn/tailscale',
  wireless: '/wireless',
  nat: '/firewall/nat',
  protection: '/firewall/protection',
  certificates: '/system/certificates',
}

const auth = useAuthStore()
const config = useConfigStore()
const route = useRoute()
const router = useRouter()
const editing = ref(null)
const open = ref(false)
const counters = ref({})

/**
 * The zone lives in the URL hash, like the tabs of a page: a reload comes
 * back to the rules you were reading, and a link from somewhere that names a
 * rule can land on the zone holding it. A zone that is deleted while you are
 * on it takes you to the first one rather than to an empty table.
 */
const zone = useTabHash(() => config.zones.map((z) => z.name))

const rules = computed(() => config.rulesForZone(zone.value))

/**
 * The rules Ostiole adds on its own, around the zone's rules and in the
 * order the kernel meets them. They follow the draft, so a zone that just
 * got anti-lockout shows it at once. Like the counters they are decoration.
 */
const {
  rows: system,
  error: systemError,
  updatedAt: systemRead,
} = useDraftRows((draft) => api.systemRules(draft))

/**
 * The table waits for the first read of the system rules. They land above
 * and below the zone's own rules, and arriving after those they would push
 * them down the moment they appear. A read that fails shows the zone's
 * rules alone.
 */
const ready = computed(() => Boolean(systemRead.value || systemError.value))

function inZone(row) {
  return !row.zones?.length || row.zones.includes(zone.value)
}
const before = computed(() => system.value.filter((s) => !s.after && inZone(s)))
const after = computed(() => system.value.filter((s) => s.after && inZone(s)))

/** A system rule may count under more than one kernel rule; they are summed. */
function packets(row) {
  let total = 0
  let counted = false
  for (const key of row.keys ?? []) {
    const c = counters.value[key]
    if (!c) continue
    total += c.packets
    counted = true
  }
  return counted ? total : ''
}

/**
 * Which interfaces the selected zone covers. Worth showing: a VLAN assigned
 * to an existing zone has no tab of its own here, and without this the only
 * clue is that the zone list is shorter than the interface list.
 */
const members = computed(() =>
  config.interfaces.filter((i) => i.zone === zone.value).map((i) => i.name),
)

// Counters are decoration; a failed read leaves the column blank.
useAsync(
  async () => {
    counters.value = await api.counters()
  },
  { interval: 5000, immediate: true },
)

function describe(ep, ports) {
  let who = 'any'
  if (ep?.self) who = 'this firewall'
  else if (ep?.alias) who = `@${ep.alias}`
  else if (ep?.addresses?.length) who = ep.addresses.join(', ')
  if (ep?.notAddresses) who = `not ${who}`
  let p = ''
  if (ep?.portAlias) p = `@${ep.portAlias}`
  else if (ep?.ports?.length) p = ep.ports.join(', ')
  if (p && ep?.notPorts) p = `not ${p}`
  return ports && p ? `${who} : ${p}` : who
}

function add() {
  editing.value = null
  open.value = true
}

function edit(rule) {
  editing.value = rule
  open.value = true
}

function toggle(rule) {
  config.upsertRule({ ...rule, enabled: !rule.enabled })
}

/**
 * An alias page can send somebody here to write the rule that uses a
 * list: `?block=<alias>` opens a new drop rule with it filled in — as the
 * source for an address list, as the ports for a port list. The query is
 * dropped straight away, so a reload does not reopen the dialog on a rule
 * that may already have been saved.
 */
onMounted(() => {
  const name = route.query.block
  if (typeof name !== 'string' || !name) return
  const alias = config.aliases.find((a) => a.name === name)
  router.replace({ path: route.path, hash: route.hash })
  if (!alias) return
  editing.value = {
    description: `Block ${name}`,
    enabled: true,
    zone: zone.value,
    action: 'drop',
    protocol: alias.type === 'ports' ? 'tcp+udp' : 'any',
    log: false,
    source: alias.type === 'ports' ? {} : { alias: name },
    destination: alias.type === 'ports' ? { portAlias: name } : {},
  }
  open.value = true
})
</script>

<template>
  <div class="space-y-5">
    <AppNotice v-if="!config.zones.length">
      No zones yet, so a rule has nowhere to go.
      <RouterLink to="/interfaces#zones" class="link">Add one</RouterLink>.
    </AppNotice>
    <AppNotice v-else-if="zone && !members.length">
      No interface is in zone <span class="font-mono">{{ zone }}</span
      >, so these rules match nothing.
      <RouterLink to="/interfaces" class="link">Assign one</RouterLink>.
    </AppNotice>

    <SectionCard title="Rules" :count="rules.length" flush>
      <template #intro>
        First match wins, top to bottom. Unmatched traffic is dropped.
        <template v-if="members.length">
          Zone <span class="font-mono">{{ zone }}</span> covers
          <span class="font-mono">{{ members.join(', ') }}</span
          >. To give one of them rules of its own,
          <RouterLink to="/interfaces" class="link">move it to its own zone</RouterLink>.
        </template>
      </template>
      <template #actions>
        <div class="flex gap-1 rounded-md border border-line p-0.5" role="group" aria-label="Zone">
          <button
            v-for="z in config.zones"
            :key="z.name"
            type="button"
            class="rounded px-2.5 py-1 font-mono text-sm"
            :class="z.name === zone ? 'bg-surface-2 text-ink' : 'text-ink-muted hover:text-ink'"
            :aria-pressed="z.name === zone"
            @click="zone = z.name"
          >
            {{ z.name }}
          </button>
        </div>
        <button
          v-if="!auth.readOnly"
          type="button"
          class="btn-secondary"
          :disabled="!zone"
          @click="add"
        >
          <Plus class="size-4" aria-hidden="true" /> Add rule
        </button>
      </template>
      <table class="table">
        <thead>
          <tr>
            <th class="w-8"></th>
            <th>Action</th>
            <th>Protocol</th>
            <th>Source</th>
            <th>Destination</th>
            <th>Via</th>
            <th>Description</th>
            <th class="text-right">Packets</th>
            <th></th>
          </tr>
        </thead>
        <tbody v-if="!ready">
          <tr>
            <td colspan="9" class="text-ink-muted">Reading…</td>
          </tr>
        </tbody>
        <TransitionGroup v-else name="row" tag="tbody">
          <SystemRuleRow
            v-for="s in before"
            :key="`system:${s.chain}:${s.description}`"
            :rule="s"
            :packets="packets(s)"
            :to="SETTINGS[s.setting]"
          />
          <tr v-if="rules.length === 0" key="empty" class="row-static">
            <td colspan="9" class="text-ink-muted">No rules of your own in this zone.</td>
          </tr>
          <tr
            v-for="(r, i) in rules"
            :key="r.id"
            :class="{ 'opacity-50': !r.enabled, 'row-changed': config.isChanged('rules', r.id) }"
          >
            <td>
              <input
                type="checkbox"
                class="size-4 rounded border-line-2"
                :checked="r.enabled"
                :disabled="auth.readOnly"
                :aria-label="`Enable ${r.id}`"
                @change="toggle(r)"
              />
            </td>
            <td>
              <span
                class="badge"
                :class="{ 'badge-ok': r.action === 'accept', 'badge-warn': r.action !== 'accept' }"
                >{{ r.action }}</span
              >
              <span v-if="r.log" class="badge ml-1">log</span>
              <span v-if="r.schedule" class="badge ml-1">{{ r.schedule }}</span>
            </td>
            <td class="font-mono text-code">{{ r.protocol }}</td>
            <td class="font-mono text-code">{{ describe(r.source, true) }}</td>
            <td class="font-mono text-code">{{ describe(r.destination, true) }}</td>
            <td class="font-mono text-code">
              <span v-if="r.gateway" class="badge" :title="`Routed through ${r.gateway}`"
                >→ {{ r.gateway }}</span
              >
              <template v-else>{{ r.destZone ?? '' }}</template>
              <span
                v-if="r.priority"
                :class="[tierBadge(r.priority), r.gateway || r.destZone ? 'ml-1' : '']"
                :title="`Priority ${tierLabel(r.priority)}`"
                >{{ tierLabel(r.priority) }}</span
              >
            </td>
            <td>{{ r.description }}</td>
            <td class="text-right font-mono text-code tabular-nums">
              {{ counters[r.id]?.packets ?? '' }}
            </td>
            <td class="text-right whitespace-nowrap">
              <template v-if="!auth.readOnly">
                <button
                  type="button"
                  class="icon-btn"
                  :disabled="i === 0"
                  :aria-label="`Move ${r.id} up`"
                  @click="config.moveRule(r.id, -1)"
                >
                  <ArrowUp class="size-4" />
                </button>
                <button
                  type="button"
                  class="icon-btn"
                  :disabled="i === rules.length - 1"
                  :aria-label="`Move ${r.id} down`"
                  @click="config.moveRule(r.id, 1)"
                >
                  <ArrowDown class="size-4" />
                </button>
              </template>
              <button type="button" class="link ml-2" @click="edit(r)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete rule ${r.id}?`"
                :description="r.description"
                @confirm="config.removeRule(r.id)"
              />
            </td>
          </tr>
          <SystemRuleRow
            v-for="s in after"
            :key="`system:${s.chain}:${s.description}`"
            :rule="s"
            :packets="packets(s)"
            :to="SETTINGS[s.setting]"
          />
        </TransitionGroup>
      </table>
    </SectionCard>

    <RuleDialog v-model:open="open" :rule="editing" :zone="zone" />
  </div>
</template>
