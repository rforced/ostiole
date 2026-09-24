<script setup>
import { Plus } from 'lucide-vue-next'
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useDraftRows } from '@/lib/draft'
import { useConfigStore } from '@/stores/config'
import OneToOneDialog from '@/views/firewall/OneToOneDialog.vue'
import OutboundDialog from '@/views/firewall/OutboundDialog.vue'
import PortForwardDialog from '@/views/firewall/PortForwardDialog.vue'
import { tierBadge, tierLabel } from '@/views/firewall/shaping/tiers'

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

const MODE_HINTS = {
  automatic: 'Masquerades IPv4 leaving every external zone. Nothing to configure.',
  hybrid: 'The rules below apply first, then the automatic masquerade.',
  manual: 'Only the rules below and 1:1 mappings. Anything else leaves untranslated.',
  disabled: 'Only 1:1 mappings are translated on the way out.',
}
const mode = computed({
  get: () => outbound.value.mode,
  set: (v) => config.setOutboundMode(v),
})

/**
 * What automatic and hybrid mode write on their own, read for the draft:
 * the masquerade on each external zone. The mode is checked here too,
 * because a draft that does not validate keeps the rows of the last one
 * that did, which may have been in another mode.
 */
const { rows: automatic } = useDraftRows((draft) => api.systemNat(draft))
const showAutomatic = computed(
  () => (mode.value === 'automatic' || mode.value === 'hybrid') && automatic.value.length > 0,
)

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
  <div class="space-y-5">
    <SectionCard title="Port forwards" :count="forwards.length" flush>
      <template #actions>
        <button type="button" class="btn-secondary" @click="addPf">
          <Plus class="size-4" aria-hidden="true" /> Add port forward
        </button>
      </template>
      <table class="table">
        <thead>
          <tr>
            <th>Zone</th>
            <th>Protocol</th>
            <th>Ports</th>
            <th>Destination</th>
            <th>Description</th>
            <th></th>
          </tr>
        </thead>
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="forwards.length === 0" key="empty" class="row-static">
            <td colspan="6" class="text-ink-muted">No port forwards.</td>
          </tr>
          <tr
            v-for="pf in forwards"
            :key="pf.id"
            :class="{
              'opacity-50': !pf.enabled,
              'row-changed': config.isChanged('nat.portForwards', pf.id),
            }"
          >
            <td class="font-mono">{{ pf.zone }}</td>
            <td class="font-mono text-code">{{ pf.protocol }}</td>
            <td class="font-mono text-code">{{ pf.ports.join(', ') }}</td>
            <td class="font-mono text-code">
              {{ pf.target }}<span v-if="pf.targetPort">:{{ pf.targetPort }}</span>
            </td>
            <td>
              {{ pf.description }}
              <span v-if="pf.reflection" class="badge ml-1">reflection</span>
              <span
                v-if="pf.priority"
                :class="[tierBadge(pf.priority), 'ml-1']"
                :title="`Priority ${tierLabel(pf.priority)}`"
                >{{ tierLabel(pf.priority) }}</span
              >
            </td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="editPf(pf)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete port forward ${pf.id}?`"
                :description="pf.description"
                @confirm="config.removePortForward(pf.id)"
              />
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <SectionCard title="1:1 NAT" :count="oneToOne.length" flush>
      <template #actions>
        <button type="button" class="btn-secondary" @click="addOne">
          <Plus class="size-4" aria-hidden="true" /> Add 1:1 NAT
        </button>
      </template>
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
        <TransitionGroup name="row" tag="tbody">
          <tr v-if="oneToOne.length === 0" key="empty" class="row-static">
            <td colspan="5" class="text-ink-muted">No 1:1 mappings.</td>
          </tr>
          <tr
            v-for="o in oneToOne"
            :key="o.id"
            :class="{
              'opacity-50': !o.enabled,
              'row-changed': config.isChanged('nat.oneToOne', o.id),
            }"
          >
            <td class="font-mono">{{ o.zone }}</td>
            <td class="font-mono text-code">{{ o.external }}</td>
            <td class="font-mono text-code">{{ o.internal }}</td>
            <td>{{ o.description }}</td>
            <td class="text-right whitespace-nowrap">
              <button type="button" class="link" @click="editOne(o)">Edit</button>
              <ConfirmButton
                class="ml-3"
                label="Delete"
                :question="`Delete 1:1 NAT ${o.id}?`"
                :description="o.description"
                @confirm="config.removeOneToOne(o.id)"
              />
            </td>
          </tr>
        </TransitionGroup>
      </table>
    </SectionCard>

    <SectionCard title="Outbound NAT" flush>
      <template v-if="mode === 'manual' || mode === 'hybrid'" #actions>
        <button type="button" class="btn-secondary" @click="addOb">
          <Plus class="size-4" aria-hidden="true" /> Add outbound rule
        </button>
      </template>
      <div class="px-4 py-3">
        <FormField id="ob-mode" label="Mode" :hint="MODE_HINTS[mode]" class="max-w-lg">
          <select id="ob-mode" v-model="mode" class="input">
            <option value="automatic">Automatic</option>
            <option value="hybrid">Hybrid</option>
            <option value="manual">Manual</option>
            <option value="disabled">Disabled</option>
          </select>
        </FormField>
      </div>
      <template v-if="mode === 'manual' || mode === 'hybrid'">
        <table class="table">
          <thead>
            <tr>
              <th>Zone</th>
              <th>Sources</th>
              <th>Destination</th>
              <th>Leaves as</th>
              <th>Description</th>
              <th></th>
            </tr>
          </thead>
          <TransitionGroup name="row" tag="tbody">
            <tr v-if="(outbound.rules ?? []).length === 0" key="empty" class="row-static">
              <td colspan="6" class="text-ink-muted">
                {{
                  mode === 'hybrid'
                    ? 'No rules, so this behaves like automatic.'
                    : 'No rules, so nothing is translated.'
                }}
              </td>
            </tr>
            <tr
              v-for="r in outbound.rules ?? []"
              :key="r.id"
              :class="{
                'opacity-50': !r.enabled,
                'row-changed': config.isChanged('nat.outbound.rules', r.id),
              }"
            >
              <td class="font-mono">{{ r.zone }}</td>
              <td class="font-mono text-code">{{ r.source?.join(', ') || 'anything' }}</td>
              <td class="font-mono text-code">{{ r.destination?.join(', ') || 'anywhere' }}</td>
              <td class="font-mono text-code">
                <span v-if="r.noNat" class="badge">not translated</span>
                <template v-else-if="r.address">{{ r.address }}</template>
                <span v-else class="text-ink-muted">the interface address</span>
              </td>
              <td>{{ r.description }}</td>
              <td class="text-right whitespace-nowrap">
                <button type="button" class="link" @click="editOb(r)">Edit</button>
                <ConfirmButton
                  class="ml-3"
                  label="Delete"
                  :question="`Delete outbound rule ${r.id}?`"
                  :description="r.description"
                  @confirm="config.removeOutboundRule(r.id)"
                />
              </td>
            </tr>
          </TransitionGroup>
        </table>
      </template>
    </SectionCard>

    <SectionCard v-if="showAutomatic" title="Automatic rules" flush>
      <table class="table">
        <thead>
          <tr>
            <th>Zone</th>
            <th>Sources</th>
            <th>Destination</th>
            <th>Leaves as</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in automatic" :key="s.zone" class="row-static">
            <td class="font-mono">
              {{ s.zone }}
              <span class="ml-1 text-ink-muted">{{ (s.interfaces ?? []).join(', ') }}</span>
            </td>
            <td class="font-mono text-code">{{ s.source }}</td>
            <td class="font-mono text-code">{{ s.destination }}</td>
            <td class="font-mono text-code">
              <template v-if="s.to">{{ s.to }}</template>
              <span v-else class="text-ink-muted">the interface address</span>
            </td>
            <td class="text-right whitespace-nowrap">
              <span class="badge">locked</span>
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <PortForwardDialog v-model:open="pfOpen" :forward="pfEditing" />
    <OneToOneDialog v-model:open="oneOpen" :entry="oneEditing" />
    <OutboundDialog v-model:open="obOpen" :rule="obEditing" />
  </div>
</template>
