<script setup>
import { AlertTriangle, CircleCheck, CircleX, Info } from 'lucide-vue-next'

/** A boxed line the page wants read before anything under it. */
defineProps({
  kind: {
    type: String,
    default: 'warn',
    validator: (k) => ['info', 'warn', 'bad', 'ok'].includes(k),
  },
  title: { type: String, default: '' },
  /**
   * Overrides the role. A notice that appears in answer to something the
   * admin just did is `status`, so a screen reader announces it; the
   * default is the quiet `note`, or `alert` for a failure.
   */
  role: { type: String, default: '' },
})

const ICON = { info: Info, warn: AlertTriangle, bad: CircleX, ok: CircleCheck }
const TONE = {
  info: 'border-info-line bg-info-soft text-info-ink [&>svg]:text-info',
  warn: 'border-warn-line bg-warn-soft text-warn-ink [&>svg]:text-warn',
  bad: 'border-bad-line bg-bad-soft text-bad-ink [&>svg]:text-bad',
  ok: 'border-ok-line bg-ok-soft text-ok-ink [&>svg]:text-ok',
}
</script>

<template>
  <div
    class="flex gap-3 rounded-lg border p-3 text-sm"
    :class="TONE[kind]"
    :role="role || (kind === 'bad' ? 'alert' : 'note')"
  >
    <component :is="ICON[kind]" class="mt-0.5 size-4 shrink-0" aria-hidden="true" />
    <div class="min-w-0 space-y-0.5">
      <p v-if="title" class="font-medium">{{ title }}</p>
      <div v-if="$slots.default"><slot /></div>
    </div>
  </div>
</template>
