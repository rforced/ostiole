<script setup>
import { ArrowDown, ArrowUp, Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useReorder } from '@/lib/reorder'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import AccessDialog from '@/views/services/proxy/AccessDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const proxy = computed(() => config.proxy)

function numberField(key, fallback) {
  return computed({
    get: () => proxy.value[key] || fallback,
    set: (v) => {
      const n = Number(v)
      config.setProxy({ [key]: n && n !== fallback ? n : undefined })
    },
  })
}

const httpPort = numberField('httpPort', 80)
const httpsPort = numberField('httpsPort', 443)

/**
 * The access list, zone by zone in the order the zones are listed: a rule
 * is read against the others of its zone, so that is what Move up and
 * Move down step through.
 */
const access = computed(() => {
  const list = proxy.value.access ?? []
  const zones = config.zones.map((z) => z.name)
  const known = list.filter((a) => zones.includes(a.zone))
  const out = []
  for (const zone of zones) {
    const own = known.filter((a) => a.zone === zone)
    own.forEach((a, i) => out.push({ line: a, first: i === 0, last: i === own.length - 1 }))
  }
  // A rule whose zone is gone still shows, to be fixed or deleted.
  for (const a of list.filter((x) => !zones.includes(x.zone)))
    out.push({ line: a, first: true, last: true })
  return out
})

const { moveClass, reorder } = useReorder()
const editing = ref(null)
const open = ref(false)

function add() {
  editing.value = null
  open.value = true
}

function edit(line) {
  editing.value = line
  open.value = true
}

function toggle(line) {
  config.upsertProxyAccess({ ...line, enabled: !line.enabled })
}

/** The ports a rule names: the listeners, then the routes. */
function ports(a) {
  const names = (a.ports ?? []).map((p) => (p === 'http' ? 'HTTP' : 'HTTPS'))
  return [...names, ...(a.routes ?? [])].join(', ')
}

function source(a) {
  const src = a.source ?? {}
  let who = 'any'
  if (src.alias) who = `@${src.alias}`
  else if (src.addresses?.length) who = src.addresses.join(', ')
  return src.notAddresses ? `not ${who}` : who
}

function label(a) {
  return a.description || a.id
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Service" :locked="auth.readOnly">
      <div class="space-y-5">
        <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
          <FormField id="proxy-http" label="HTTP port" hint="80 is the default.">
            <input
              id="proxy-http"
              v-model.number="httpPort"
              type="number"
              min="1"
              max="65535"
              class="input"
            />
          </FormField>
          <FormField id="proxy-https" label="HTTPS port" hint="443 is the default.">
            <input
              id="proxy-https"
              v-model.number="httpsPort"
              type="number"
              min="1"
              max="65535"
              class="input"
            />
          </FormField>
        </div>

        <ToggleRow
          id="proxy-http3"
          :model-value="proxy.http3 === true"
          label="HTTP/3"
          hint="UDP on the HTTPS port as well."
          @update:model-value="config.setProxy({ http3: $event })"
        />
      </div>
    </SectionCard>

    <SectionCard
      title="Access"
      :count="access.length"
      intro="Read top to bottom within a zone, first match wins."
      flush
    >
      <template v-if="!auth.readOnly" #actions>
        <button type="button" class="btn-secondary" :disabled="!config.zones.length" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add rule
        </button>
      </template>
      <table class="table table-stack">
        <thead>
          <tr>
            <th class="w-8"></th>
            <th>Zone</th>
            <th>Ports</th>
            <th>Source</th>
            <th>Action</th>
            <th>Description</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup tag="tbody" :css="false" :move-class="moveClass">
          <tr v-if="!access.length" key="empty">
            <td colspan="7" class="text-ink-muted">
              Nothing reaches the proxy until a rule accepts it.
            </td>
          </tr>
          <tr
            v-for="{ line: a, first, last } in access"
            :key="a.id"
            :class="{
              'opacity-50': !a.enabled,
              'row-changed': config.isChanged('services.proxy.access', a.id),
            }"
          >
            <td data-label="">
              <input
                type="checkbox"
                class="size-4 rounded border-line-2"
                :checked="a.enabled"
                :disabled="auth.readOnly"
                :aria-label="`Enable ${label(a)}`"
                @change="toggle(a)"
              />
            </td>
            <td class="font-mono text-code" data-label="Zone">{{ a.zone }}</td>
            <td class="font-mono text-code" data-label="Ports">{{ ports(a) }}</td>
            <td class="font-mono text-code" data-label="Source">{{ source(a) }}</td>
            <td data-label="Action">
              <span
                class="badge"
                :class="{ 'badge-ok': a.action === 'accept', 'badge-warn': a.action !== 'accept' }"
                >{{ a.action }}</span
              >
              <span v-if="a.log" class="badge ml-1">log</span>
            </td>
            <td data-label="Description">{{ a.description }}</td>
            <td class="actions" data-label="">
              <template v-if="!auth.readOnly">
                <button
                  type="button"
                  class="icon-btn"
                  :disabled="first"
                  :aria-label="`Move ${label(a)} up`"
                  @click="reorder(() => config.moveProxyAccess(a.id, -1))"
                >
                  <ArrowUp class="size-4" />
                </button>
                <button
                  type="button"
                  class="icon-btn"
                  :disabled="last"
                  :aria-label="`Move ${label(a)} down`"
                  @click="reorder(() => config.moveProxyAccess(a.id, 1))"
                >
                  <ArrowDown class="size-4" />
                </button>
              </template>
              <button type="button" class="link-action" @click="edit(a)">
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                label="Delete"
                :question="`Delete the ${a.action} rule for ${ports(a)} on ${a.zone}?`"
                :description="a.description"
                @confirm="config.removeProxyAccess(a.id)"
              />
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <AccessDialog v-model:open="open" :line="editing" />
  </div>
</template>
