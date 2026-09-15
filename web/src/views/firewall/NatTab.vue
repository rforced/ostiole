<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'
import OneToOneDialog from '@/views/firewall/OneToOneDialog.vue'
import OutboundDialog from '@/views/firewall/OutboundDialog.vue'
import PortForwardDialog from '@/views/firewall/PortForwardDialog.vue'

const config = useConfigStore()
const pfEditing = ref(null)
const pfOpen = ref(false)
const obEditing = ref(null)
const obOpen = ref(false)
const oneEditing = ref(null)
const oneOpen = ref(false)

const forwards = computed(() => config.nat.portForwards ?? [])
const oneToOne = computed(() => config.nat.oneToOne ?? [])
const outbound = computed(() => config.nat.outbound)
const mode = computed({
  get: () => outbound.value.mode,
  set: (v) => config.setOutboundMode(v),
})

function addPf() {
  pfEditing.value = null
  pfOpen.value = true
}
function editPf(pf) {
  pfEditing.value = pf
  pfOpen.value = true
}
function addOne() {
  oneEditing.value = null
  oneOpen.value = true
}
function editOne(o) {
  oneEditing.value = o
  oneOpen.value = true
}
function addOb() {
  obEditing.value = null
  obOpen.value = true
}
function editOb(r) {
  obEditing.value = r
  obOpen.value = true
}
</script>

<template>
  <div class="space-y-8">
    <section class="space-y-3" aria-labelledby="pf-title">
      <div class="flex items-center gap-3">
        <h2 id="pf-title" class="font-medium">Port forwards</h2>
        <button type="button" class="btn-secondary" @click="addPf">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add port forward
        </button>
      </div>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Zone</th>
              <th>Protocol</th>
              <th>Ports</th>
              <th>Target</th>
              <th>Description</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="forwards.length === 0">
              <td colspan="6" class="text-neutral-500">No port forwards.</td>
            </tr>
            <tr v-for="pf in forwards" :key="pf.id" :class="{ 'opacity-50': !pf.enabled }">
              <td class="font-mono">{{ pf.zone }}</td>
              <td class="font-mono text-xs">{{ pf.protocol }}</td>
              <td class="font-mono text-xs">{{ pf.ports.join(', ') }}</td>
              <td class="font-mono text-xs">
                {{ pf.target }}<span v-if="pf.targetPort">:{{ pf.targetPort }}</span>
              </td>
              <td>
                {{ pf.description }}
                <span v-if="pf.reflection" class="badge ml-1">reflection</span>
              </td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editPf(pf)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  confirm-label="Delete forward?"
                  @confirm="config.removePortForward(pf.id)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="space-y-3" aria-labelledby="one-title">
      <div class="flex items-center gap-3">
        <h2 id="one-title" class="font-medium">1:1 NAT</h2>
        <button type="button" class="btn-secondary" @click="addOne">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add 1:1 NAT
        </button>
      </div>
      <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
        <table class="table">
          <thead>
            <tr>
              <th>Zone</th>
              <th>External</th>
              <th>Internal</th>
              <th>Description</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="oneToOne.length === 0">
              <td colspan="5" class="text-neutral-500">
                No 1:1 mappings. They give one inside host its own outside address.
              </td>
            </tr>
            <tr v-for="o in oneToOne" :key="o.id" :class="{ 'opacity-50': !o.enabled }">
              <td class="font-mono">{{ o.zone }}</td>
              <td class="font-mono text-xs">{{ o.external }}</td>
              <td class="font-mono text-xs">{{ o.internal }}</td>
              <td>{{ o.description }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editOne(o)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  confirm-label="Delete mapping?"
                  @confirm="config.removeOneToOne(o.id)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="space-y-3" aria-labelledby="ob-title">
      <h2 id="ob-title" class="font-medium">Outbound NAT</h2>
      <FormField
        id="ob-mode"
        label="Mode"
        hint="Automatic masquerades IPv4 leaving every external zone."
      >
        <select id="ob-mode" v-model="mode" class="input max-w-xs">
          <option value="automatic">Automatic</option>
          <option value="manual">Manual rules</option>
          <option value="disabled">Disabled</option>
        </select>
      </FormField>
      <template v-if="mode === 'manual'">
        <button type="button" class="btn-secondary" @click="addOb">
          <Plus class="mr-1 size-4" aria-hidden="true" /> Add outbound rule
        </button>
        <div class="overflow-x-auto rounded-lg border border-neutral-200 dark:border-neutral-800">
          <table class="table">
            <thead>
              <tr>
                <th>Zone</th>
                <th>Sources</th>
                <th>Description</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="(outbound.rules ?? []).length === 0">
                <td colspan="4" class="text-neutral-500">
                  No manual rules: nothing is masqueraded.
                </td>
              </tr>
              <tr
                v-for="r in outbound.rules ?? []"
                :key="r.id"
                :class="{ 'opacity-50': !r.enabled }"
              >
                <td class="font-mono">{{ r.zone }}</td>
                <td class="font-mono text-xs">{{ r.source?.join(', ') || 'all IPv4' }}</td>
                <td>{{ r.description }}</td>
                <td class="text-right whitespace-nowrap">
                  <button type="button" class="link" @click="editOb(r)">Edit</button>
                  <ConfirmButton
                    class="ml-3"
                    label="Delete"
                    confirm-label="Delete rule?"
                    @confirm="config.removeOutboundRule(r.id)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </section>

    <PortForwardDialog v-model:open="pfOpen" :forward="pfEditing" />
    <OneToOneDialog v-model:open="oneOpen" :entry="oneEditing" />
    <OutboundDialog v-model:open="obOpen" :rule="obEditing" />
  </div>
</template>
