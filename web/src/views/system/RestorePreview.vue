<script setup>
import AppNotice from '@/components/AppNotice.vue'
import ChangeList from '@/components/ChangeList.vue'

/**
 * What a backup holds and what restoring it would change. It is the same
 * panel whether the backup came from a file or out of the bucket, and
 * nothing it shows has been applied.
 */
defineProps({
  /** @type {import('vue').PropType<{summary: object, changes: object[]}>} */
  pending: { type: Object, required: true },
})
defineEmits(['load', 'cancel'])

/** "1 rule" or "3 rules"; pass the plural where an -s will not do. */
function count(n, one, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`
}
</script>

<template>
  <AppNotice>
    <p>
      Backup of
      <span class="font-mono">{{ pending.summary.hostname || 'an unnamed router' }}</span>
      taken {{ new Date(pending.summary.createdAt).toLocaleString() }}
      <template v-if="pending.summary.ostiole"> by Ostiole {{ pending.summary.ostiole }}</template
      >.
      <template v-if="pending.summary.note">“{{ pending.summary.note }}”</template>
    </p>
    <p>
      {{ count(pending.summary.zones, 'zone') }},
      {{ count(pending.summary.interfaces, 'interface') }},
      {{ count(pending.summary.rules, 'rule') }},
      {{ count(pending.summary.aliases, 'alias', 'aliases') }},
      {{ count(pending.summary.gateways, 'gateway') }}.
      <template v-if="pending.summary.users">
        Also {{ count(pending.summary.users, 'account') }}, which only
        <span class="font-mono">ostiole restore --with-users</span> restores.
      </template>
    </p>
    <p v-if="pending.summary.redacted">
      Secrets were left out of this backup. The draft needs them before it applies.
    </p>
    <div>
      <p class="mb-1 font-medium">What it would change:</p>
      <ChangeList
        :changes="pending.changes"
        :limit="12"
        empty-label="Nothing: this backup matches the saved configuration."
      />
    </div>
    <div class="flex gap-2 pt-1">
      <button type="button" class="btn-primary" @click="$emit('load')">Load into draft</button>
      <button type="button" class="btn-secondary" @click="$emit('cancel')">Cancel</button>
    </div>
  </AppNotice>
</template>
