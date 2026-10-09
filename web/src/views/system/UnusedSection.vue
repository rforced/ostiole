<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import ErrorLine from '@/components/ErrorLine.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { emptyText, useAsync } from '@/lib/async'
import { adminOnly, useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

/**
 * What the draft leaves unused, removable one by one or together, and what
 * it switches off, which is kept on purpose. The server walks the draft;
 * removing goes through the store's own removers and the apply bar.
 */

const auth = useAuthStore()
const config = useConfigStore()

/** How long the list waits after the last edit before asking again. */
const DEBOUNCE_MS = 400

/** Each kind as a sentence names it, and the page it is edited on. */
const KINDS = {
  alias: ['alias', '/firewall/aliases'],
  schedule: ['schedule', '/firewall/schedules'],
  pool: ['pool', '/services/proxy#pools'],
  'waf-profile': ['WAF profile', '/services/proxy#waf'],
  certificate: ['certificate', '/system/certificates#certificates'],
  'acme-account': ['ACME account', '/system/certificates#accounts'],
  'dns-provider': ['DNS provider', '/system/dns-providers'],
  'gateway-group': ['gateway group', '/routing#gateways'],
  gateway: ['gateway', '/routing#gateways'],
  zone: ['zone', '/interfaces#zones'],
  radio: ['radio', '/wireless#radios'],
  'static-lease': ['static lease', '/services/dhcp#v4'],
  rule: ['rule', '/firewall/rules'],
  cron: ['cron job', '/system/crons'],
  interface: ['interface', '/interfaces#interfaces'],
  'wireguard-peer': ['WireGuard peer', '/vpn/wireguard#peers'],
  'one-to-one-nat': ['1:1 NAT', '/firewall/nat'],
  'outbound-nat': ['outbound rule', '/firewall/nat'],
  'port-forward': ['port forward', '/firewall/nat'],
  'static-route': ['static route', '/routing#routes'],
  'proxy-access': ['proxy access rule', '/services/proxy#service'],
  'proxy-site': ['site', '/services/proxy#sites'],
  'proxy-route': ['route', '/services/proxy#routes'],
  'ddns-record': ['Dynamic DNS record', '/services/ddns'],
  'block-list': ['block list', '/services/dns#blocking'],
}

const nounOf = (kind) => KINDS[kind]?.[0] ?? kind
const labelOf = (kind) => nounOf(kind).charAt(0).toUpperCase() + nounOf(kind).slice(1)

/**
 * An interface is edited where its kind is: a tunnel under WireGuard, a
 * network under Wireless. A rule is on its zone's tab.
 */
function pageOf(it) {
  if (it.kind === 'interface') {
    const i = config.findInterface(it.id)
    if (i?.wireguard) return '/vpn/wireguard#tunnels'
    if (i?.tailscale) return '/vpn/tailscale#settings'
    if (i?.wireless) return '/wireless#networks'
  }
  if (it.kind === 'rule') {
    const r = byKey(config.rules, 'id', it.id)
    if (r) return `/firewall/rules#${r.zone}`
  }
  return KINDS[it.kind]?.[1] ?? ''
}

const byKey = (list, key, id) => (list ?? []).find((x) => x[key] === id)

/** An interface's own Delete, as the page it is deleted on has it. */
function interfaceDelete(i) {
  const dependents = config.interfaceDependents(i.name)
  if (i.wireguard)
    return {
      confirm: {
        question: `Delete tunnel ${i.name}?`,
        description: 'Peers lose their way in once this is applied. The private key is not kept.',
        dependents: [...(i.wireguard.peers ?? []).map((p) => `peer ${p.name}`), ...dependents],
        typed: i.name,
      },
      run: () => config.removeTunnel(i.name),
    }
  let description =
    'Its addresses, zone, and settings go on the next apply. The device itself stays.'
  if (i.tailscale)
    description = 'The daemon stops on the next apply. The node stays in the admin console.'
  else if (i.wireless) description = 'It stops being served on the next apply.'
  return {
    confirm: {
      question: `Delete ${i.wireless?.ssid ?? i.name} from the configuration?`,
      description,
      dependents,
      typed: i.name,
    },
    run: () => config.removeInterface(i.name),
  }
}

/**
 * Each kind's Delete as its own page has it: the question, what goes with
 * it, the name typed back where that page asks for it, and the remover.
 * Each returns nothing once the draft no longer has the item.
 */
const DELETES = {
  alias: (id, d) => {
    const a = byKey(d.aliases, 'name', id)
    return (
      a && {
        confirm: {
          question: `Delete alias ${id}?`,
          description: a.description,
          disabled: config.aliasReferences(id).length > 0,
        },
        run: () => config.removeAlias(id),
      }
    )
  },
  schedule: (id, d) => {
    const s = byKey(d.schedules, 'name', id)
    return (
      s && {
        confirm: {
          question: `Delete schedule ${id}?`,
          description: s.description,
          disabled: config.scheduleReferences(id).length > 0,
        },
        run: () => config.removeSchedule(id),
      }
    )
  },
  pool: (id, d) =>
    byKey(d.services?.proxy?.pools, 'id', id) && {
      confirm: {
        question: `Delete pool ${id}?`,
        typed: id,
        disabled: config.poolDependents(id).length > 0,
      },
      run: () => config.removePool(id),
    },
  'waf-profile': (id, d) =>
    byKey(d.services?.proxy?.wafProfiles, 'id', id) && {
      confirm: {
        question: `Delete WAF profile ${id}?`,
        dependents: config.profileDependents(id),
        dependentsLabel: 'Left uninspected',
        typed: id,
      },
      run: () => config.removeProfile(id),
    },
  certificate: (id, d) =>
    byKey(d.certificates, 'id', id) && {
      confirm: {
        question: `Delete certificate ${id}?`,
        description: 'Its files are deleted within a few seconds of the apply.',
        dependents: config.certificateDependents(id),
        dependentsLabel: 'Goes back to the built-in certificate',
        typed: id,
        disabled: !auth.isAdmin && d.system?.management?.certificate === id,
      },
      run: () => config.removeCertificate(id),
    },
  'acme-account': (id, d) =>
    byKey(d.acme?.accounts, 'id', id) && {
      confirm: {
        question: `Delete ACME account ${id}?`,
        description: 'Nothing is deleted at the CA.',
        typed: id,
        disabled: !auth.isAdmin || config.accountDependents(id).length > 0,
      },
      run: () => config.removeAcmeAccount(id),
    },
  'dns-provider': (id, d) =>
    byKey(d.dnsProviders, 'id', id) && {
      confirm: {
        question: `Delete DNS provider ${id}?`,
        description: 'The credentials go with it.',
        typed: id,
        disabled: !auth.isAdmin || config.providerDependents(id).length > 0,
      },
      run: () => config.removeDnsProvider(id),
    },
  'gateway-group': (id, d) => {
    const g = byKey(d.gatewayGroups, 'name', id)
    return (
      g && {
        confirm: {
          question: `Delete group ${id}?`,
          description: g.description,
          dependents: config.groupDependents(id),
          dependentsLabel: 'Also changed',
        },
        run: () => config.removeGatewayGroup(id),
      }
    )
  },
  gateway: (id, d) => {
    const g = byKey(d.gateways, 'name', id)
    return (
      g && {
        confirm: {
          question: `Delete gateway ${id}?`,
          description: g.description,
          dependents: config.gatewayDependents(id),
          dependentsLabel: 'Also changed',
        },
        run: () => config.removeGateway(id),
      }
    )
  },
  zone: (id, d) => {
    const z = byKey(d.zones, 'name', id)
    return (
      z && {
        confirm: {
          question: `Delete zone ${id}?`,
          dependents: config.zoneDependents(id),
          typed: id,
          disabled: config.zoneInterfaces(id).length > 0 || (z.antiLockout && !auth.isAdmin),
        },
        run: () => config.removeZone(id),
      }
    )
  },
  radio: (id, d) =>
    byKey(d.wireless?.radios, 'name', id) && {
      confirm: {
        question: `Delete radio ${id} from the configuration?`,
        description: 'It stops transmitting on the next apply. The card itself stays.',
        dependents: config.radioDependents(id),
        typed: id,
      },
      run: () => config.removeRadio(id),
    },
  'static-lease': (id, d) => {
    const mac = id.toLowerCase()
    const l = (d.services?.dhcp?.staticLeases ?? []).find((x) => x.mac.toLowerCase() === mac)
    return (
      l && {
        confirm: { question: `Delete static lease ${l.mac}?`, description: l.description },
        run: () => config.removeStaticLease(l.mac),
      }
    )
  },
  rule: (id, d) => {
    const r = byKey(d.rules, 'id', id)
    return (
      r && {
        confirm: { question: `Delete rule ${id}?`, description: r.description },
        run: () => config.removeRule(id),
      }
    )
  },
  cron: (id, d) => {
    const c = byKey(d.crons, 'id', id)
    return (
      c && {
        confirm: {
          question: `Delete cron job ${c.description || id}?`,
          disabled: !auth.isAdmin && (c.kind === 'command' || c.withUsers === true),
        },
        run: () => config.removeCron(id),
      }
    )
  },
  interface: (id) => {
    const i = config.findInterface(id)
    return i && interfaceDelete(i)
  },
  'wireguard-peer': (id) => {
    const at = id.indexOf('/')
    const tunnel = id.slice(0, at)
    const peer = id.slice(at + 1)
    return (
      byKey(config.findInterface(tunnel)?.wireguard?.peers, 'name', peer) && {
        confirm: {
          question: `Delete peer ${peer}?`,
          dependents: config.peerDependents(tunnel, peer),
        },
        run: () => config.removePeer(tunnel, peer),
      }
    )
  },
  'one-to-one-nat': (id, d) => {
    const o = byKey(d.nat?.oneToOne, 'id', id)
    return (
      o && {
        confirm: { question: `Delete 1:1 NAT ${id}?`, description: o.description },
        run: () => config.removeOneToOne(id),
      }
    )
  },
  'outbound-nat': (id, d) => {
    const o = byKey(d.nat?.outbound?.rules, 'id', id)
    return (
      o && {
        confirm: { question: `Delete outbound rule ${id}?`, description: o.description },
        run: () => config.removeOutboundRule(id),
      }
    )
  },
  'port-forward': (id, d) => {
    const pf = byKey(d.nat?.portForwards, 'id', id)
    return (
      pf && {
        confirm: { question: `Delete port forward ${id}?`, description: pf.description },
        run: () => config.removePortForward(id),
      }
    )
  },
  'static-route': (id, d) => {
    const r = byKey(d.routes, 'id', id)
    return (
      r && {
        confirm: { question: `Delete route ${id}?`, description: r.description },
        run: () => config.removeRoute(id),
      }
    )
  },
  'proxy-access': (id, d) => {
    const a = byKey(d.services?.proxy?.access, 'id', id)
    if (!a) return null
    const ports = [
      ...(a.ports ?? []).map((p) => (p === 'http' ? 'HTTP' : 'HTTPS')),
      ...(a.routes ?? []),
    ].join(', ')
    return {
      confirm: {
        question: `Delete the ${a.action} rule for ${ports} on ${a.zone}?`,
        description: a.description,
      },
      run: () => config.removeProxyAccess(id),
    }
  },
  'proxy-site': (id, d) => {
    const s = byKey(d.services?.proxy?.sites, 'id', id)
    return (
      s && {
        confirm: {
          question: `Delete site ${id}?`,
          description: (s.hosts ?? []).join(', '),
          dependents: config.siteDependents(id),
          typed: id,
        },
        run: () => config.removeSite(id),
      }
    )
  },
  'proxy-route': (id, d) => {
    const r = byKey(d.services?.proxy?.routes, 'id', id)
    if (!r) return null
    const named = config.routeDependents(id)
    const port = `${r.protocol} port ${r.port}`
    return {
      confirm: {
        question: `Delete route ${id}?`,
        description: named.length ? `${port}. Access rules naming it: ${named.join(', ')}.` : port,
        typed: id,
      },
      run: () => config.removeProxyRoute(id),
    }
  },
  'ddns-record': (id, d) => {
    const r = byKey(d.services?.ddns?.records, 'id', id)
    return (
      r && {
        confirm: {
          question: `Delete dynamic DNS record ${r.name}?`,
          description: 'The record at the provider stays as it is.',
        },
        run: () => config.removeDdnsRecord(r),
      }
    )
  },
  'block-list': (id, d) => {
    const l = byKey(d.blocking?.lists, 'name', id)
    return (
      l && {
        confirm: { question: `Delete list ${id}?`, description: l.description },
        run: () => config.removeBlockList(id),
      }
    )
  },
}

const deletion = (it) => DELETES[it.kind]?.(it.id, config.draft ?? {}) || null

const unused = ref([])
const disabled = ref([])
/** Bumped by every edit, so an answer about an older draft is dropped. */
let edits = 0
/** The draft has changed since the lists were read: from the edit until its answer lands. */
const behind = ref(false)

const load = useAsync(
  async () => {
    const seen = edits
    const out = await api.config.unused(config.draft)
    if (seen !== edits) return
    unused.value = out?.unused ?? []
    disabled.value = out?.disabled ?? []
    behind.value = false
  },
  { immediate: true },
)

let timer = 0
watch(
  () => config.draft,
  () => {
    edits++
    behind.value = true
    window.clearTimeout(timer)
    timer = window.setTimeout(load.run, DEBOUNCE_MS)
  },
  { deep: true },
)
onBeforeUnmount(() => window.clearTimeout(timer))

const row = (it) => ({ ...it, page: pageOf(it), del: deletion(it) })
const groups = computed(() => [
  { title: 'Unused', empty: 'No unused items.', rows: unused.value.map(row) },
  { title: 'Disabled', empty: 'No disabled items.', rows: disabled.value.map(row) },
])

/** The unused items whose own Delete the draft allows now. */
const removable = computed(() =>
  unused.value
    .map((it) => ({ it, del: deletion(it) }))
    .filter(({ del }) => del && !del.confirm.disabled),
)
const removeLabel = computed(() => {
  const n = removable.value.length
  return `Remove ${n} unused item${n === 1 ? '' : 's'}`
})
const removedNames = computed(() =>
  removable.value.flatMap(({ it }) => [`${nounOf(it.kind)} ${it.name}`, ...(it.takes ?? [])]),
)

/** What the server keeps to an admin. */
function needsAdmin(it) {
  if (it.kind === 'dns-provider' || it.kind === 'acme-account') return true
  return it.kind === 'zone' && byKey(config.zones, 'name', it.id)?.antiLockout === true
}
const removeLocked = computed(() => !auth.isAdmin && unused.value.some(needsAdmin))

const same = (a, b) => a.kind === b.kind && a.id === b.id

function remove(r) {
  r.del.run()
  unused.value = unused.value.filter((x) => !same(x, r))
  disabled.value = disabled.value.filter((x) => !same(x, r))
}

function removeAll() {
  const runs = removable.value
  const n = runs.length
  config.undoable(`Removed ${n} unused item${n === 1 ? '' : 's'}.`, () => {
    for (const { del } of runs) del.run()
  })
  unused.value = unused.value.filter((x) => !runs.some(({ it }) => same(it, x)))
}
</script>

<template>
  <SectionCard
    title="Unused"
    :count="unused.length"
    intro="Nothing in the configuration uses these items. Disabled items are kept on purpose and listed below them."
    flush
  >
    <template v-if="removable.length" #actions>
      <ConfirmButton
        :label="removeLabel"
        :question="`${removeLabel}?`"
        :dependents="removedNames"
        dependents-label="Removed"
        confirm-label="Remove"
        :disabled="removeLocked || behind"
        :title="
          removeLocked
            ? adminOnly('remove', 'DNS providers, ACME accounts or anti-lockout zones')
            : undefined
        "
        @confirm="removeAll"
      />
    </template>
    <div v-if="load.error.value" class="card-strip">
      <ErrorLine>{{ load.error.value }}</ErrorLine>
    </div>
    <table class="table table-stack">
      <thead>
        <tr>
          <th>Kind</th>
          <th>Name</th>
          <th>Why</th>
          <th></th>
        </tr>
      </thead>
      <tbody v-for="(g, gi) in groups" :key="g.title">
        <tr class="max-sm:border-b-0 max-sm:py-0">
          <th
            colspan="4"
            scope="rowgroup"
            class="border-b border-line bg-surface-2/60 px-3 py-2 text-xs font-medium tracking-wide text-ink-muted uppercase max-sm:block"
            :class="{ 'border-t': gi > 0 }"
          >
            {{ g.title }}
          </th>
        </tr>
        <tr v-if="!g.rows.length">
          <td colspan="4" class="text-ink-muted">{{ emptyText(load, g.empty) }}</td>
        </tr>
        <tr v-for="r in g.rows" :key="`${r.kind}:${r.id}`">
          <td data-label="Kind">{{ labelOf(r.kind) }}</td>
          <td data-label="Name">
            <div :class="{ 'font-mono text-code': r.name === r.id }">{{ r.name }}</div>
            <div v-if="r.name !== r.id" class="font-mono text-xs text-ink-muted">{{ r.id }}</div>
          </td>
          <td data-label="Why">
            <template v-if="gi === 0">
              {{ r.why }}
              <div v-if="r.takes?.length" class="text-xs text-ink-muted">
                Takes {{ r.takes.join(', ') }}
              </div>
            </template>
            <template v-else>Switched off</template>
          </td>
          <td class="actions" data-label="">
            <RouterLink v-if="r.page" :to="r.page" class="link-action">Open</RouterLink>
            <ConfirmButton
              v-if="r.del"
              label="Delete"
              v-bind="r.del.confirm"
              @confirm="remove(r)"
            />
          </td>
        </tr>
      </tbody>
    </table>
  </SectionCard>
</template>
