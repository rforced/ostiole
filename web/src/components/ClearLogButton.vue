<script setup>
import { computed } from 'vue'

import ConfirmButton from '@/components/ConfirmButton.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'

/**
 * The one Clear for a log, which empties the router's log and deletes its
 * files once the shared dialog says yes. Only an admin clears: an operator
 * sees it greyed, a viewer not at all.
 */
const props = defineProps({
  /** What a sentence calls the log: "DHCP log". */
  name: { type: String, required: true },
  /** What it holds one of: "message". */
  noun: { type: String, default: '' },
  /** The log is fed from the journal, which keeps its own lines. */
  journal: { type: Boolean, default: false },
  /** In place of the sentence built from noun, for a Clear that takes more. */
  description: { type: String, default: '' },
  busy: { type: Boolean, default: false },
})
const emit = defineEmits(['confirm'])
const auth = useAuthStore()
const config = useConfigStore()

/** Kept in files as well, by the configuration the router runs. */
const inFiles = computed(() => Boolean(config.saved?.system?.logging?.files?.enabled))

const text = computed(() => {
  const dropped =
    props.description ||
    `Every ${props.noun} it holds is dropped${inFiles.value ? ', and its files are deleted' : ''}.`
  return props.journal ? `${dropped} The journal keeps its own copy.` : dropped
})
</script>

<template>
  <ConfirmButton
    label="Clear"
    :question="`Clear the ${name}?`"
    :description="text"
    :busy="busy"
    :disabled="!auth.isAdmin"
    :title="auth.isAdmin ? undefined : 'Only an admin can clear it.'"
    @confirm="emit('confirm')"
  />
</template>
