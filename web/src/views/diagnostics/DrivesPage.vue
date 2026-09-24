<script setup>
import { LoaderCircle } from 'lucide-vue-next'
import { computed, onMounted, ref, watch } from 'vue'

import AppDisclosure from '@/components/AppDisclosure.vue'
import AppNotice from '@/components/AppNotice.vue'
import PageHeader from '@/components/PageHeader.vue'
import RefreshButton from '@/components/RefreshButton.vue'
import SectionCard from '@/components/SectionCard.vue'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/async'
import { formatBytes, formatCount } from '@/lib/format'
import { LEVELS } from '@/lib/meter'
import { useConfirmStore } from '@/stores/confirm'
import { useToastStore } from '@/stores/toast'

const confirm = useConfirmStore()
const toast = useToastStore()

const status = ref(null)
const drives = computed(() => status.value?.drives ?? [])

const load = useAsync(async () => {
  status.value = await api.diagnostics.drives()
})
onMounted(load.run)

// Which test was asked for, by drive. The drive reports that a test is
// running but not which one, and the page is the only thing that knows.
const started = ref({})

/** One place for what a button does, so its failure lands in one alert. */
const action = useAsync((run) => run())
/** The button whose request is in flight, as drive:what. */
const doing = ref('')
const busyOn = (drive, what) => doing.value === `${drive.name}:${what}`

async function act(drive, what, run) {
  doing.value = `${drive.name}:${what}`
  await action.run(run)
  doing.value = ''
}

function replaceDrive(updated) {
  if (!updated || !status.value) return
  const i = status.value.drives.findIndex((d) => d.name === updated.name)
  if (i >= 0) status.value.drives[i] = updated
}

const running = computed(() => drives.value.filter((d) => d.selfTest?.running))

// A test that is running is re-read on its own, without refreshing the
// whole page; useAsync pauses it while the tab is hidden.
const poll = useAsync(
  async () => {
    const fresh = await Promise.all(running.value.map((d) => api.diagnostics.drive(d.name)))
    fresh.forEach(replaceDrive)
  },
  { interval: 10000, autostart: false },
)
watch(
  () => running.value.length,
  (n) => (n ? poll.start() : poll.stop()),
)

async function start(drive, kind) {
  if (kind === 'long') {
    const hint = minutes(drive.selfTest?.extendedMinutes)
    const ok = await confirm.ask({
      question: `Run an extended test on ${drive.name}?`,
      description: `${hint ? hint + ' ' : ''}The drive answers slower until it finishes.`,
      confirmLabel: 'Run',
      danger: false,
    })
    if (!ok) return
  }
  await act(drive, kind, async () => {
    started.value = { ...started.value, [drive.name]: kind }
    replaceDrive(await api.diagnostics.selfTest(drive.name, kind))
  })
}

function abort(drive) {
  act(drive, 'abort', async () => {
    replaceDrive(await api.diagnostics.abortSelfTest(drive.name))
  })
}

function report(drive) {
  act(drive, 'report', async () => {
    const { blob, name } = await api.diagnostics.driveReport(drive.name)
    // The API is behind a CSRF header, so a plain link cannot fetch it.
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.click()
    URL.revokeObjectURL(url)
    toast.show(`Downloaded ${name}.`)
  })
}

/** How long a test takes, in the unit that reads at a glance. */
function minutes(n) {
  if (!n) return ''
  if (n < 90) return `About ${n} minute${n === 1 ? '' : 's'}.`
  const hours = Math.round(n / 60)
  return `About ${hours} hour${hours === 1 ? '' : 's'}.`
}

const TEST_LABELS = { short: 'Short', long: 'Extended', conveyance: 'Conveyance' }

/** The one line over the buttons: what the drive is doing, or last did. */
function testLine(drive) {
  const t = drive.selfTest ?? {}
  if (t.running) {
    const label = TEST_LABELS[started.value[drive.name]] ?? 'Self'
    return `${label} test running, ${t.remaining}% left.`
  }
  if (!t.supported) return 'This drive has no self-test.'
  const last = drive.testLog?.[0]
  if (!last) return 'No test has run.'
  return `Last test: ${last.type.toLowerCase()}, ${last.status.toLowerCase()}, at ${last.hours} hours.`
}

function healthLabel(drive) {
  if (drive.health === 'passed') return 'passed'
  return drive.health === 'failed' ? 'failed' : 'no SMART'
}

function healthTone(drive) {
  if (drive.health === 'passed') return 'badge-ok'
  return drive.health === 'failed' ? 'badge-bad' : ''
}

function kindLabel(drive) {
  switch (drive.kind) {
    case 'ssd':
      return 'SSD'
    case 'nvme':
      return 'NVMe'
    case 'hdd':
      return drive.rpm ? `HDD, ${formatCount(drive.rpm)} rpm` : 'HDD'
    default:
      return 'unknown'
  }
}

function attributeSubtitle(drive) {
  const base = 'Reallocated, pending and uncorrectable sectors above zero are the ones that matter.'
  return drive.inDatabase
    ? base
    : `${base} This drive is not in the drive database, so vendor attributes are unnamed.`
}

/**
 * A full read fails some minor log on most drives, so "partial" alone is
 * not news. It is worth a badge when the table the page is built on is
 * what went missing.
 */
function missingReadings(drive) {
  return drive.partial && !drive.attributes?.length && !drive.nvme
}

/** A critical attribute that has started counting is worth the ink. */
function rawTone(a) {
  return a.critical && a.raw > 0 ? 'text-warn' : ''
}

const hasLBA = (drive) => (drive.testLog ?? []).some((e) => e.lba !== undefined && e.lba !== null)
</script>

<template>
  <div class="space-y-5">
    <PageHeader>
      <RefreshButton
        :busy="load.busy.value"
        :updated-at="load.updatedAt.value"
        @click="load.run()"
      />
    </PageHeader>

    <p v-if="load.error.value || action.error.value" role="alert" class="text-sm text-bad">
      {{ load.error.value || action.error.value }}
    </p>

    <template v-if="status && !drives.length">
      <AppNotice v-if="!status.tool">
        Reading a drive needs a package this router lacks. Run
        <code class="font-mono text-code">ostiole repair</code> on the console.
      </AppNotice>
      <AppNotice v-else-if="!status.root">
        This daemon is not running as root, so it cannot read the drives.
      </AppNotice>
      <p v-else class="text-sm text-ink-muted">No drive on this router answers SMART.</p>
    </template>

    <SectionCard v-for="d in drives" :key="d.name">
      <template #title>
        {{ d.model }}
        <span class="badge font-mono">{{ d.name }}</span>
      </template>
      <template #actions>
        <template v-if="d.selfTest?.supported && !d.selfTest.running">
          <button
            type="button"
            class="btn-secondary"
            :disabled="action.busy.value"
            :title="minutes(d.selfTest.shortMinutes)"
            :aria-busy="busyOn(d, 'short')"
            @click="start(d, 'short')"
          >
            <LoaderCircle
              v-if="busyOn(d, 'short')"
              class="size-4 animate-spin"
              aria-hidden="true"
            />
            Short test
          </button>
          <button
            type="button"
            class="btn-secondary"
            :disabled="action.busy.value"
            :title="minutes(d.selfTest.extendedMinutes)"
            :aria-busy="busyOn(d, 'long')"
            @click="start(d, 'long')"
          >
            <LoaderCircle v-if="busyOn(d, 'long')" class="size-4 animate-spin" aria-hidden="true" />
            Extended test
          </button>
          <button
            v-if="d.selfTest.conveyance"
            type="button"
            class="btn-secondary"
            :disabled="action.busy.value"
            :title="minutes(d.selfTest.conveyanceMinutes)"
            :aria-busy="busyOn(d, 'conveyance')"
            @click="start(d, 'conveyance')"
          >
            <LoaderCircle
              v-if="busyOn(d, 'conveyance')"
              class="size-4 animate-spin"
              aria-hidden="true"
            />
            Conveyance test
          </button>
        </template>
        <button
          v-else-if="d.selfTest?.supported"
          type="button"
          class="btn-secondary"
          :disabled="action.busy.value"
          :aria-busy="busyOn(d, 'abort')"
          @click="abort(d)"
        >
          <LoaderCircle v-if="busyOn(d, 'abort')" class="size-4 animate-spin" aria-hidden="true" />
          Abort
        </button>
        <button
          type="button"
          class="btn-secondary"
          :disabled="action.busy.value"
          :aria-busy="busyOn(d, 'report')"
          @click="report(d)"
        >
          <LoaderCircle v-if="busyOn(d, 'report')" class="size-4 animate-spin" aria-hidden="true" />
          Full report
        </button>
      </template>

      <div class="space-y-4">
        <div class="grid gap-x-8 gap-y-4 md:grid-cols-2">
          <dl class="kv">
            <dt>Health</dt>
            <dd>
              <span class="badge" :class="healthTone(d)">{{ healthLabel(d) }}</span>
              <span v-if="missingReadings(d)" class="badge badge-warn ml-2">
                some readings missing
              </span>
            </dd>
            <dt>Serial</dt>
            <dd class="font-mono text-code">{{ d.serial || '—' }}</dd>
            <dt>Firmware</dt>
            <dd class="font-mono text-code">{{ d.firmware || '—' }}</dd>
            <dt>Capacity</dt>
            <dd>{{ formatBytes(d.capacity) }}</dd>
            <dt>Kind</dt>
            <dd>{{ kindLabel(d) }}</dd>
            <template v-if="d.interface">
              <dt>Interface</dt>
              <dd>{{ d.interface }}</dd>
            </template>
            <template v-if="d.temperature != null">
              <dt>Temperature</dt>
              <dd>
                {{ d.temperature }} °C
                <span v-if="d.temperatureMax != null" class="text-ink-muted">
                  · highest {{ d.temperatureMax }}
                </span>
              </dd>
            </template>
            <template v-if="d.powerOnHours != null">
              <dt>Powered on</dt>
              <dd>{{ formatCount(d.powerOnHours) }} hours</dd>
            </template>
            <template v-if="d.powerCycles != null">
              <dt>Power cycles</dt>
              <dd>{{ formatCount(d.powerCycles) }}</dd>
            </template>
            <template v-if="d.wear != null">
              <dt>Wear</dt>
              <dd>{{ d.wear }}% used</dd>
            </template>
            <template v-if="d.spare != null">
              <dt>Spare</dt>
              <dd>{{ d.spare }}% left</dd>
            </template>
            <template v-if="d.written != null">
              <dt>Written</dt>
              <dd>{{ formatBytes(d.written) }}</dd>
            </template>
            <template v-if="d.read != null">
              <dt>Read</dt>
              <dd>{{ formatBytes(d.read) }}</dd>
            </template>
          </dl>

          <dl v-if="d.nvme" class="kv">
            <dt>Critical warning</dt>
            <dd>
              <span class="badge" :class="d.nvme.criticalWarning ? 'badge-bad' : 'badge-ok'">
                {{ d.nvme.criticalWarning || 'none' }}
              </span>
            </dd>
            <dt>Spare</dt>
            <dd>{{ d.nvme.availableSpare }}%, warns at {{ d.nvme.spareThreshold }}%</dd>
            <dt>Used</dt>
            <dd>{{ d.nvme.percentageUsed }}%</dd>
            <dt>Unsafe shutdowns</dt>
            <dd>{{ formatCount(d.nvme.unsafeShutdowns) }}</dd>
            <dt>Media errors</dt>
            <dd>{{ formatCount(d.nvme.mediaErrors) }}</dd>
            <dt>Error log entries</dt>
            <dd>{{ formatCount(d.nvme.errorLogEntries) }}</dd>
            <template v-if="d.nvme.sensors?.length">
              <dt>Sensors</dt>
              <dd>{{ d.nvme.sensors.map((s) => `${s} °C`).join(', ') }}</dd>
            </template>
          </dl>
        </div>

        <div class="max-w-md space-y-2">
          <p>{{ testLine(d) }}</p>
          <div
            v-if="d.selfTest?.running"
            class="meter"
            :class="LEVELS.ok.track"
            role="meter"
            :aria-label="`${d.name} self-test`"
            :aria-valuenow="100 - d.selfTest.remaining"
            aria-valuemin="0"
            aria-valuemax="100"
          >
            <div
              class="meter-fill"
              :class="LEVELS.ok.fill"
              :style="{ width: `${100 - d.selfTest.remaining}%` }"
            />
          </div>
        </div>

        <AppDisclosure v-if="d.attributes?.length" :label="`Attributes · ${d.attributes.length}`">
          <p class="max-w-3xl text-ink-muted">{{ attributeSubtitle(d) }}</p>
          <div class="overflow-x-auto">
            <table class="table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>Attribute</th>
                  <th>Value</th>
                  <th>Worst</th>
                  <th>Threshold</th>
                  <th class="text-right">Raw</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="a in d.attributes" :key="a.id">
                  <td class="font-mono text-code">{{ a.id }}</td>
                  <td>
                    {{ a.name }}
                    <span v-if="a.whenFailed === 'now'" class="badge badge-bad ml-1">
                      failing now
                    </span>
                    <span v-else-if="a.whenFailed === 'past'" class="badge badge-warn ml-1">
                      failed before
                    </span>
                    <span v-if="a.prefail" class="badge ml-1">pre-fail</span>
                  </td>
                  <td class="font-mono text-code">{{ a.value }}</td>
                  <td class="font-mono text-code">{{ a.worst }}</td>
                  <td class="font-mono text-code">{{ a.threshold }}</td>
                  <td class="text-right font-mono text-code" :class="rawTone(a)">
                    {{ a.rawString }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </AppDisclosure>

        <AppDisclosure label="Self-test log">
          <div class="overflow-x-auto">
            <table class="table">
              <thead>
                <tr>
                  <th>Test</th>
                  <th>Result</th>
                  <th>Power-on hours</th>
                  <th v-if="hasLBA(d)">LBA</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="!d.testLog?.length">
                  <td :colspan="hasLBA(d) ? 4 : 3" class="text-ink-muted">No test has run.</td>
                </tr>
                <tr v-for="(e, i) in d.testLog" :key="i">
                  <td>{{ e.type }}</td>
                  <td>
                    <span class="badge" :class="e.passed ? 'badge-ok' : 'badge-bad'">
                      {{ e.status }}
                    </span>
                  </td>
                  <td class="font-mono text-code">{{ formatCount(e.hours) }}</td>
                  <td v-if="hasLBA(d)" class="font-mono text-code">{{ e.lba ?? '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </AppDisclosure>

        <AppDisclosure :label="`Errors · ${formatCount(d.errorCount)} logged`">
          <div class="overflow-x-auto">
            <table class="table">
              <thead>
                <tr>
                  <th>Number</th>
                  <th>Power-on hours</th>
                  <th>Description</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="!d.errors?.length">
                  <td colspan="3" class="text-ink-muted">No errors logged.</td>
                </tr>
                <tr v-for="(e, i) in d.errors" :key="i">
                  <td class="font-mono text-code">{{ e.number }}</td>
                  <td class="font-mono text-code">{{ formatCount(e.hours) }}</td>
                  <td>{{ e.description }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </AppDisclosure>
      </div>
    </SectionCard>
  </div>
</template>
