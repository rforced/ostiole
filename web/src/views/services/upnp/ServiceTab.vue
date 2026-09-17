<script setup>
import { ArrowDown, ArrowUp, Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
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
  <div class="space-y-6">
    <label class="flex items-center gap-2 text-sm">
      <input v-model="upnp.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
      <span class="font-medium">Port mapping enabled</span>
      <span class="text-neutral-500">Clients open their own inbound ports.</span>
    </label>

    <fieldset class="space-y-2 text-sm">
      <legend class="subsection-title">Protocols</legend>
      <label class="flex items-center gap-2">
        <input v-model="upnp.igd" type="checkbox" class="size-4 rounded border-neutral-300" />
        UPnP IGD
        <span class="text-neutral-500">Consoles and Windows ask with this.</span>
      </label>
      <label class="flex items-center gap-2">
        <input v-model="upnp.pcp" type="checkbox" class="size-4 rounded border-neutral-300" />
        PCP and NAT-PMP
        <span class="text-neutral-500">Apple devices ask with this.</span>
      </label>
      <p
        v-if="upnp.enabled && !upnp.igd && !upnp.pcp"
        class="text-sm text-amber-700 dark:text-amber-400"
      >
        With neither switched on, nothing answers.
      </p>
    </fieldset>

    <div class="grid max-w-2xl gap-4 sm:grid-cols-2">
      <FormField id="upnp-ext" label="External interface" hint="Where a mapped port is opened.">
        <select id="upnp-ext" v-model="externalInterface" class="input font-mono">
          <option value="">Choose an interface</option>
          <option v-for="i in externalChoices" :key="i.name" :value="i.name">{{ i.name }}</option>
        </select>
      </FormField>
    </div>

    <fieldset class="space-y-2 text-sm">
      <legend class="subsection-title">Clients may ask from</legend>
      <label class="flex items-center gap-2">
        <input v-model="listenAll" type="checkbox" class="size-4 rounded border-neutral-300" />
        Every interface outside external zones
      </label>
      <div v-if="!listenAll" class="ml-6 flex flex-wrap gap-4">
        <label v-for="i in insideChoices" :key="i.name" class="flex items-center gap-2">
          <input
            type="checkbox"
            class="size-4 rounded border-neutral-300"
            :checked="(upnp.interfaces ?? []).includes(i.name)"
            @change="toggleInterface(i.name, $event.target.checked)"
          />
          <span class="font-mono">{{ i.name }}</span>
        </label>
      </div>
    </fieldset>

    <section class="space-y-3" aria-labelledby="acl-title">
      <div class="flex items-center gap-3">
        <h2 id="acl-title" class="section-title">Access list</h2>
        <button type="button" class="btn-secondary" @click="add">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add entry
        </button>
      </div>
      <label class="flex items-center gap-2 text-sm">
        <input
          v-model="upnp.defaultDeny"
          type="checkbox"
          class="size-4 rounded border-neutral-300"
        />
        Refuse anything no entry allows
      </label>
      <p class="text-sm text-neutral-500">
        Read top to bottom, first match wins. IPv6 clients are not matched.
      </p>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
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
              <td colspan="6" class="text-neutral-500">
                {{
                  upnp.defaultDeny
                    ? 'No entries. Every request is refused.'
                    : 'No entries. Every request is allowed.'
                }}
              </td>
            </tr>
            <tr
              v-for="(r, i) in acl"
              :key="i"
              :class="{ 'row-changed': config.isChanged('services.upnp.acl', i) }"
            >
              <td>
                <span
                  class="badge"
                  :class="
                    r.action === 'deny'
                      ? 'bg-red-100 text-red-900 dark:bg-red-950 dark:text-red-200'
                      : 'bg-emerald-100 text-emerald-900 dark:bg-emerald-950 dark:text-emerald-200'
                  "
                  >{{ r.action }}</span
                >
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
      </div>
    </section>

    <AclDialog v-model:open="open" :index="editing" :entry="editing >= 0 ? acl[editing] : null" />
  </div>
</template>
