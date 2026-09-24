<script setup>
import { computed, ref } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import FormField from '@/components/FormField.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { COUNTRIES } from '@/lib/countries'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import RadioDialog from '@/views/wireless/RadioDialog.vue'

const auth = useAuthStore()
const config = useConfigStore()
const live = ref([])
const editing = ref(null)
const dialogOpen = ref(false)

const load = useAsync(
  async () => {
    live.value = (await api.wireless.radios()).radios ?? []
  },
  { immediate: true },
)

const BANDS = { '2g': '2.4 GHz', '5g': '5 GHz', '6g': '6 GHz' }

/** The cards have answered once; before that a configured radio would show as absent. */
const loaded = computed(() => load.updatedAt.value > 0 || Boolean(load.error.value))

/** Every radio this router has, whether or not it is configured. */
const rows = computed(() => {
  const seen = new Set()
  const out = live.value.map((card) => {
    seen.add(card.name)
    return { card, cfg: config.radios.find((r) => r.name === card.name) ?? null }
  })
  for (const r of config.radios) if (!seen.has(r.name)) out.push({ card: null, cfg: r })
  return out
})

/** The country the card itself is following, which may not be ours yet. */
const country = computed(() => config.wirelessCountry)

/** The bands a network may be started on; a card's 6 GHz is often not one. */
function bandsOf(card) {
  if (!card) return '—'
  return Object.entries(card.bands ?? {})
    .filter(([, b]) => b.serves !== false)
    .map(([name]) => BANDS[name] ?? name)
    .join(', ')
}

function describe(row) {
  if (!row.cfg) return 'not configured'
  const ch = row.cfg.channel ? `channel ${row.cfg.channel}` : 'automatic channel'
  return `${BANDS[row.cfg.band] ?? row.cfg.band} · ${ch} · ${row.cfg.width} MHz · ${row.cfg.standard}`
}

function edit(row) {
  editing.value = row
  dialogOpen.value = true
}
</script>

<template>
  <div class="space-y-5">
    <SectionCard title="Radios" :count="rows.length" flush>
      <template #actions>
        <RefreshButton
          :busy="load.busy.value"
          :updated-at="load.updatedAt.value"
          @click="load.run()"
        />
      </template>
      <div class="card-strip space-y-2">
        <FormField id="wifi-country" label="Country" hint="Every radio follows it.">
          <select
            id="wifi-country"
            class="input w-64"
            :value="country"
            :disabled="auth.readOnly"
            @change="config.setWirelessCountry($event.target.value)"
          >
            <option value="">Not set</option>
            <option v-for="c in COUNTRIES" :key="c.code" :value="c.code.toUpperCase()">
              {{ c.name }} ({{ c.code.toUpperCase() }})
            </option>
          </select>
        </FormField>
        <p v-if="load.error.value" role="alert" class="text-bad">{{ load.error.value }}</p>
      </div>
      <table class="table">
        <thead>
          <tr>
            <th>Radio</th>
            <th>Driver</th>
            <th>Bands</th>
            <th>Networks</th>
            <th>Configured</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!loaded">
            <td colspan="6" class="text-ink-muted">Reading…</td>
          </tr>
          <tr v-else-if="!rows.length">
            <td colspan="6" class="text-ink-muted">No radios on this router.</td>
          </tr>
          <tr v-for="row in rows" :key="row.card?.name ?? row.cfg.name">
            <td>
              <div class="font-mono font-medium">{{ row.card?.name ?? row.cfg.name }}</div>
              <div v-if="row.card?.mac" class="text-xs text-ink-muted">{{ row.card.mac }}</div>
            </td>
            <td class="font-mono text-code">{{ row.card?.driver || '—' }}</td>
            <td>{{ bandsOf(row.card) }}</td>
            <td>
              <span v-if="!row.card" class="text-ink-muted">—</span>
              <span v-else-if="row.card.maxNetworks === 1">one network</span>
              <span v-else>up to {{ row.card.maxNetworks }} networks</span>
            </td>
            <td>
              {{ describe(row)
              }}<span v-if="row.cfg && !row.cfg.enabled" class="ml-1 text-ink-muted"
                >(disabled)</span
              >
            </td>
            <td class="text-right whitespace-nowrap">
              <button
                v-if="row.cfg || !auth.readOnly"
                type="button"
                class="link"
                @click="edit(row)"
              >
                {{ auth.readOnly ? 'View' : 'Edit' }}
              </button>
              <ConfirmButton
                v-if="row.cfg"
                class="ml-3"
                label="Delete"
                :question="`Delete radio ${row.cfg.name} from the configuration?`"
                description="It stops transmitting on the next apply. The card itself stays."
                :dependents="config.radioDependents(row.cfg.name)"
                dependents-label="Also deleted"
                :typed="row.cfg.name"
                @confirm="config.removeRadio(row.cfg.name)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </SectionCard>

    <RadioDialog
      v-model:open="dialogOpen"
      :card="editing?.card ?? null"
      :radio="editing?.cfg ?? null"
    />
  </div>
</template>
