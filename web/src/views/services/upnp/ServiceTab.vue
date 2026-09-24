<script setup>
import { ArrowDown, ArrowUp, Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import AppNotice from '@/components/AppNotice.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import ToggleRow from '@/components/ToggleRow.vue'
import { useConfigStore } from '@/stores/config'
import AclDialog from '@/views/services/upnp/AclDialog.vue'

const config = useConfigStore()
const upnp = computed(() => config.ensureUPnP())

/** A mapped port is opened here, so only an external zone will do. */
const externalChoices = computed(() =>
  config.interfaces.filter((i) => i.enabled && isExternal(i.zone)),
)

function isExternal(zone) {
  return Boolean(config.zones.find((z) => z.name === zone)?.external)
}

const externalInterface = computed({
  get: () => upnp.value.externalInterface ?? '',
  set: (v) => {
    if (v) upnp.value.externalInterface = v
    else delete upnp.value.externalInterface
  },
})

/**
 * The interface still chosen after it stopped qualifying, with why, so the
 * select shows it rather than going blank.
 */
const stale = computed(() => {
  const name = externalInterface.value
  if (!name || externalChoices.value.some((i) => i.name === name)) return null
  const i = config.interfaces.find((x) => x.name === name)
  const why = !i ? 'gone' : !i.enabled ? 'disabled' : 'not external'
  return { name, why }
})

/** The interfaces clients may ask from; empty means every internal one. */
const insideChoices = computed(() =>
  config.interfaces.filter(
    (i) => i.enabled && i.zone && !isExternal(i.zone) && i.name !== upnp.value.externalInterface,
  ),
)

const listenAll = computed({
  get: () => !(upnp.value.interfaces ?? []).length,
  set: (all) => {
    if (all) delete upnp.value.interfaces
    else upnp.value.interfaces = insideChoices.value.map((i) => i.name)
  },
})

function toggleInterface(name, on) {
  const list = new Set(upnp.value.interfaces ?? [])
  if (on) list.add(name)
  else list.delete(name)
  upnp.value.interfaces = [...list]
}

const acl = computed(() => upnp.value.acl ?? [])
const editing = ref(-1)
const open = ref(false)

const keys = new WeakMap()
let lastKey = 0

/**
 * @param {object} entry
 * @returns {number} a key that stays with this entry while it is in the draft
 */
function keyOf(entry) {
  let key = keys.get(entry)
  if (!key) {
    key = ++lastKey
    keys.set(entry, key)
  }
  return key
}

function add() {
  editing.value = -1
  open.value = true
}
function edit(index) {
  editing.value = index
  open.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Service" intro="Clients open their own inbound ports.">
      <div class="space-y-5">
        <fieldset class="space-y-2">
          <legend class="group-title">Protocols</legend>
          <ToggleRow
            v-model="upnp.igd"
            label="UPnP IGD"
            hint="Consoles and Windows ask with this."
          />
          <ToggleRow
            v-model="upnp.pcp"
            label="PCP and NAT-PMP"
            hint="Apple devices ask with this."
          />
          <AppNotice v-if="upnp.enabled && !upnp.igd && !upnp.pcp">
            With neither switched on, nothing answers.
          </AppNotice>
        </fieldset>

        <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
          <FormField id="upnp-ext" label="External interface" hint="Where a mapped port is opened.">
            <select id="upnp-ext" v-model="externalInterface" class="input font-mono">
              <option value="">Choose</option>
              <option v-if="stale" :value="stale.name">{{ stale.name }} ({{ stale.why }})</option>
              <option v-for="i in externalChoices" :key="i.name" :value="i.name">
                {{ i.name }}
              </option>
            </select>
          </FormField>
        </div>

        <fieldset class="space-y-2">
          <legend class="group-title">Clients may ask from</legend>
          <ToggleRow v-model="listenAll" label="Every interface outside external zones" />
          <div v-if="!listenAll" class="ml-6 flex flex-wrap gap-4">
            <label v-for="i in insideChoices" :key="i.name" class="flex items-center gap-2">
              <input
                type="checkbox"
                class="size-4 rounded"
                :checked="(upnp.interfaces ?? []).includes(i.name)"
                @change="toggleInterface(i.name, $event.target.checked)"
              />
              <span class="font-mono">{{ i.name }}</span>
            </label>
          </div>
        </fieldset>
      </div>
    </SectionCard>

    <SectionCard
      title="Access list"
      :count="acl.length"
      intro="Read top to bottom, first match wins. IPv6 clients are not matched."
      flush
    >
      <template #actions>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="size-4" aria-hidden="true" /> Add entry
        </button>
      </template>
      <div class="card-strip">
        <ToggleRow v-model="upnp.defaultDeny" label="Refuse anything no entry allows" />
      </div>
      <table class="table">
        <thead>
          <tr>
            <th>Action</th>
            <th>External ports</th>
            <th>Client</th>
            <th>Internal ports</th>
            <th>Description</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="!acl.length" key="empty" class="row-static">
            <td colspan="6" class="text-ink-muted">
              {{
                upnp.defaultDeny
                  ? 'No entries. Every request is refused.'
                  : 'No entries. Every request is allowed.'
              }}
            </td>
          </tr>
          <tr
            v-for="(r, i) in acl"
            :key="keyOf(r)"
            :class="{ 'row-changed': config.isChanged('services.upnp.acl', i) }"
          >
            <td>
              <span class="badge" :class="r.action === 'deny' ? 'badge-bad' : 'badge-ok'">{{
                r.action
              }}</span>
            </td>
            <td class="font-mono text-code">{{ r.externalPorts }}</td>
            <td class="font-mono text-code">{{ r.source }}</td>
            <td class="font-mono text-code">{{ r.internalPorts }}</td>
            <td>{{ r.description }}</td>
            <td class="text-right whitespace-nowrap">
              <button
                type="button"
                class="icon-btn"
                :disabled="i === 0"
                :aria-label="`Move entry ${i + 1} up`"
                @click="config.moveUPnPRule(i, -1)"
              >
                <ArrowUp class="size-4" />
              </button>
              <button
                type="button"
                class="icon-btn"
                :disabled="i === acl.length - 1"
                :aria-label="`Move entry ${i + 1} down`"
                @click="config.moveUPnPRule(i, 1)"
              >
                <ArrowDown class="size-4" />
              </button>
              <button type="button" class="link ml-2" @click="edit(i)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete the ${r.action} entry for ${r.source}?`"
                :description="r.description"
                @confirm="config.removeUPnPRule(i)"
              />
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <AclDialog v-model:open="open" :index="editing" :entry="editing >= 0 ? acl[editing] : null" />
  </div>
</template>
