<script setup>
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({
  /** The card as iw reports it, or null when this router has not got it. */
  card: { type: Object, default: null },
  /** The radio as it is configured, or null for one that is not. */
  radio: { type: Object, default: null },
})
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

const BANDS = { '2g': '2.4 GHz', '5g': '5 GHz', '6g': '6 GHz' }
const STANDARDS = { ax: 'Wi-Fi 6 (ax)', ac: 'Wi-Fi 5 (ac)', n: 'Wi-Fi 4 (n)', legacy: 'Legacy' }
const WIDTHS = [20, 40, 80, 160]

function blank() {
  return { name: '', enabled: true, band: '5g', channel: 0, width: 80, standard: 'ax', power: 0 }
}

/** The bands the card has; an unknown card offers all three. */
const bands = computed(() => {
  const have = Object.keys(props.card?.bands ?? {})
  return have.length ? have : ['2g', '5g', '6g']
})

const band = computed(() => props.card?.bands?.[form.value.band] ?? null)

/** Radar and disabled channels are left out: neither can be used here. */
const channels = computed(() => (band.value?.channels ?? []).filter((c) => !c.radar && !c.disabled))

const widths = computed(() => {
  const max = band.value?.maxWidth ?? (form.value.band === '2g' ? 40 : 160)
  return WIDTHS.filter((w) => w <= max && (form.value.band !== '2g' || w <= 40))
})

const standards = computed(() => {
  const have = band.value?.standards
  if (have?.length) return have
  return form.value.band === '6g' ? ['ax'] : ['ax', 'n', 'legacy']
})

watch(
  () => [open.value, props.radio, props.card],
  () => {
    if (!open.value) return
    const name = props.radio?.name ?? props.card?.name ?? ''
    if (props.radio) {
      form.value = { ...blank(), ...props.radio, name }
      return
    }
    const first = bands.value.includes('5g') ? '5g' : (bands.value[0] ?? '2g')
    form.value = { ...blank(), name, band: first }
    form.value.width = first === '2g' ? 20 : 80
    form.value.standard = standards.value[0] ?? 'n'
  },
  { immediate: true },
)

// A band the card has not got, or a channel from another one, cannot be
// carried over when the band changes.
watch(
  () => form.value.band,
  () => {
    if (!channels.value.some((c) => c.number === form.value.channel)) form.value.channel = 0
    if (!widths.value.includes(form.value.width)) form.value.width = widths.value.at(-1) ?? 20
    if (!standards.value.includes(form.value.standard)) form.value.standard = standards.value[0]
  },
)

function save() {
  const f = form.value
  config.upsertRadio({
    name: f.name.trim(),
    enabled: f.enabled,
    band: f.band,
    channel: Number(f.channel) || 0,
    width: Number(f.width),
    standard: f.standard,
    power: Number(f.power) || 0,
  })
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="`Radio ${form.name}`"
    description="What this card transmits: where in the spectrum, how wide, and how loud."
  >
    <form class="space-y-4" @submit.prevent="save">
      <p v-if="card?.selfManaged" class="text-sm text-neutral-500">
        Takes its country from networks in range; with none, 5 GHz stays off.
      </p>
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="radio-band" label="Band">
          <select id="radio-band" v-model="form.band" class="input">
            <option v-for="b in bands" :key="b" :value="b">{{ BANDS[b] ?? b }}</option>
          </select>
        </FormField>
        <FormField id="radio-channel" label="Channel">
          <select id="radio-channel" v-model.number="form.channel" class="input">
            <option :value="0">Automatic</option>
            <option v-for="c in channels" :key="c.number" :value="c.number">
              {{ c.number }} · {{ c.mhz }} MHz
            </option>
          </select>
        </FormField>
        <FormField
          id="radio-width"
          label="Channel width"
          hint="40 MHz on 2.4 GHz drops to 20 when neighbours overlap."
        >
          <select id="radio-width" v-model.number="form.width" class="input">
            <option v-for="w in widths" :key="w" :value="w">{{ w }} MHz</option>
          </select>
        </FormField>
        <FormField id="radio-standard" label="Standard">
          <select id="radio-standard" v-model="form.standard" class="input">
            <option v-for="s in standards" :key="s" :value="s">{{ STANDARDS[s] ?? s }}</option>
          </select>
        </FormField>
        <FormField id="radio-power" label="Transmit power" hint="dBm. 0 is the maximum allowed.">
          <input
            id="radio-power"
            v-model.number="form.power"
            type="number"
            min="0"
            max="30"
            class="input w-32 font-mono"
          />
        </FormField>
      </div>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-neutral-300" />
        Enabled
      </label>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.name">Save to draft</button>
      </div>
    </form>
  </AppDialog>
</template>
