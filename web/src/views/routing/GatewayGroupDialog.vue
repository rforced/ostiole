<script setup>
import { Trash2 } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'

import AppDialog from '@/components/AppDialog.vue'
import FormField from '@/components/FormField.vue'
import { useConfigStore } from '@/stores/config'

const props = defineProps({ group: { type: Object, default: null } })
const open = defineModel('open', { type: Boolean, default: false })

const config = useConfigStore()
const form = ref(blank())

function blank() {
  return { name: '', description: '', enabled: true, onDown: 'fallback', members: [] }
}

/** Gateways not yet in the group, which is all a new member can be. */
const available = computed(() =>
  config.gateways.filter((g) => !form.value.members.some((m) => m.gateway === g.name)),
)

const tiers = computed(() => {
  const groups = new Map()
  for (const m of form.value.members) {
    const tier = Number(m.tier) || 0
    groups.set(tier, (groups.get(tier) ?? 0) + 1)
  }
  return [...groups.entries()].sort((a, b) => a[0] - b[0])
})

watch(
  () => [open.value, props.group],
  () => {
    if (!open.value) return
    form.value = props.group
      ? { ...blank(), ...props.group, members: (props.group.members ?? []).map((m) => ({ ...m })) }
      : blank()
  },
  { immediate: true },
)

function addMember() {
  const next = available.value[0]
  if (!next) return
  form.value.members.push({ gateway: next.name, tier: form.value.members.length })
}

function removeMember(index) {
  form.value.members.splice(index, 1)
}

function save() {
  const f = form.value
  const out = {
    name: f.name.trim(),
    enabled: f.enabled,
    onDown: f.onDown,
    members: f.members.map((m) => ({ gateway: m.gateway, tier: Number(m.tier) || 0 })),
  }
  if (f.description) out.description = f.description
  config.upsertGatewayGroup(out, props.group?.name ?? out.name)
  open.value = false
}
</script>

<template>
  <AppDialog
    v-model:open="open"
    :title="group ? `Group ${group.name}` : 'Add gateway group'"
    description="The lowest tier that is up carries the traffic. Gateways in the same tier share it."
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <FormField id="gg-name" label="Name" hint="Lower case, e.g. failover.">
          <input
            id="gg-name"
            v-model="form.name"
            class="input font-mono"
            required
            spellcheck="false"
          />
        </FormField>
        <FormField id="gg-desc" label="Description">
          <input id="gg-desc" v-model="form.description" class="input" />
        </FormField>
      </div>

      <FormField
        id="gg-ondown"
        label="When every gateway is down"
        hint="Block is the one to pick for a tunnel that must never leak onto the WAN."
      >
        <select id="gg-ondown" v-model="form.onDown" class="input">
          <option value="fallback">Use the normal default route</option>
          <option value="block">Drop the traffic</option>
        </select>
      </FormField>

      <section class="space-y-2">
        <div class="flex items-center gap-3">
          <h3 class="group-title">Members</h3>
          <button
            type="button"
            class="btn-secondary"
            :disabled="!available.length"
            @click="addMember"
          >
            Add gateway
          </button>
        </div>
        <p v-if="!form.members.length" class="text-sm text-ink-muted">
          No gateways yet. A group needs at least one.
        </p>
        <ul class="space-y-2">
          <li v-for="(m, i) in form.members" :key="i" class="form-row flex-nowrap">
            <FormField :id="`gg-member-${i}`" label="Gateway" class="flex-1">
              <select :id="`gg-member-${i}`" v-model="m.gateway" class="input font-mono">
                <option v-for="g in config.gateways" :key="g.name" :value="g.name">
                  {{ g.name }}
                </option>
              </select>
            </FormField>
            <FormField :id="`gg-tier-${i}`" label="Tier">
              <input
                :id="`gg-tier-${i}`"
                v-model="m.tier"
                type="number"
                min="0"
                max="255"
                class="input w-24 font-mono"
              />
            </FormField>
            <button
              type="button"
              class="link inline-flex items-center text-bad"
              :aria-label="`Remove ${m.gateway}`"
              @click="removeMember(i)"
            >
              <Trash2 class="size-4" aria-hidden="true" />
            </button>
          </li>
        </ul>
        <p v-if="tiers.length" class="text-sm text-ink-muted">
          <template v-for="([tier, count], i) in tiers" :key="tier">
            <span v-if="i">, then </span>tier {{ tier }}
            <template v-if="count > 1">shares {{ count }} gateways</template>
            <template v-else>has one gateway</template>
          </template>
        </p>
      </section>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" class="size-4 rounded border-line-2" />
        Enabled
      </label>
      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="btn-secondary" @click="open = false">Cancel</button>
        <button type="submit" class="btn-primary" :disabled="!form.members.length">
          Save to draft
        </button>
      </div>
    </form>
  </AppDialog>
</template>
