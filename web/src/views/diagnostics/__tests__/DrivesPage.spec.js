import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfirmStore } from '@/stores/confirm'
import DrivesPage from '@/views/diagnostics/DrivesPage.vue'

vi.mock('@/lib/api', () => ({
  api: {
    diagnostics: {
      drives: vi.fn(),
      drive: vi.fn(),
      selfTest: vi.fn(),
      abortSelfTest: vi.fn(),
      driveReport: vi.fn(),
    },
  },
  ApiError: class ApiError extends Error {},
}))

function drive(extra = {}) {
  return {
    name: 'sda',
    type: 'sat',
    protocol: 'ATA',
    model: 'GOFATOO 256GB SSD',
    serial: 'AA00000000000000TEST',
    firmware: 'X0528A0',
    capacity: 256060514304,
    kind: 'ssd',
    formFactor: 'M.2',
    interface: 'SATA 3.2 at 6.0 Gb/s',
    inDatabase: false,
    health: 'passed',
    smartOn: true,
    temperature: 40,
    powerOnHours: 57,
    powerCycles: 47,
    wear: 0,
    spare: 100,
    selfTest: {
      supported: true,
      conveyance: false,
      running: false,
      status: 'completed without error',
      passed: true,
      shortMinutes: 2,
      extendedMinutes: 10,
    },
    attributes: [
      {
        id: 5,
        name: 'Reallocated_Sector_Ct',
        value: 100,
        worst: 100,
        threshold: 50,
        prefail: false,
        raw: 0,
        rawString: '0',
        critical: true,
      },
      {
        id: 197,
        name: 'Current_Pending_Sector',
        value: 90,
        worst: 90,
        threshold: 50,
        prefail: false,
        raw: 3,
        rawString: '3',
        whenFailed: 'now',
        critical: true,
      },
    ],
    testLog: [
      { type: 'Short offline', status: 'Completed without error', passed: true, hours: 57 },
    ],
    errors: [],
    errorCount: 0,
    readAt: '2026-09-20T05:00:00Z',
    ...extra,
  }
}

const status = (extra = {}) => ({ tool: '7.5', root: true, drives: [drive()], ...extra })

const button = (wrapper, label) => wrapper.findAll('button').find((b) => b.text() === label)

/** Attributes and the logs sit behind folds, which unmount while closed. */
async function openFold(wrapper, label) {
  await wrapper
    .findAll('button')
    .find((b) => b.text().startsWith(label))
    .trigger('click')
  await flushPromises()
}

describe('DrivesPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setActivePinia(createPinia())
    api.diagnostics.drives.mockResolvedValue(status())
  })

  // The verdict, the figures, and the attribute that has started counting.
  it('shows what the drive says about itself', async () => {
    const wrapper = mount(DrivesPage)
    await flushPromises()
    expect(wrapper.text()).toContain('GOFATOO 256GB SSD')
    expect(wrapper.find('.badge-ok').text()).toBe('passed')
    expect(wrapper.text()).toContain('40 °C')
    expect(wrapper.text()).toContain(
      'Last test: short offline, completed without error, at 57 hours.',
    )

    await openFold(wrapper, 'Attributes')
    const pending = wrapper.findAll('tbody tr')[1]
    expect(pending.text()).toContain('failing now')
    expect(pending.find('.text-warn').text()).toBe('3')
    // The drive is not in the database, so the unnamed attributes are explained.
    expect(wrapper.text()).toContain('not in the drive database')
  })

  // A full read fails some minor log on most drives; the badge is for a
  // drive whose attribute table is what went missing.
  it('flags missing readings only when the table is gone', async () => {
    api.diagnostics.drives.mockResolvedValue(status({ drives: [drive({ partial: true })] }))
    let wrapper = mount(DrivesPage)
    await flushPromises()
    expect(wrapper.text()).not.toContain('some readings missing')

    api.diagnostics.drives.mockResolvedValue(
      status({ drives: [drive({ partial: true, attributes: [] })] }),
    )
    wrapper = mount(DrivesPage)
    await flushPromises()
    expect(wrapper.text()).toContain('some readings missing')
  })

  // Nothing to read with: the page says what is missing, not that something failed.
  it('names the missing package when there is no tool', async () => {
    api.diagnostics.drives.mockResolvedValue({ tool: '', root: true, drives: [] })
    const wrapper = mount(DrivesPage)
    await flushPromises()
    expect(wrapper.text()).toContain('ostiole repair')
    expect(wrapper.find('section').exists()).toBe(false)
  })

  it('says when the daemon is not root', async () => {
    api.diagnostics.drives.mockResolvedValue({ tool: '7.5', root: false, drives: [] })
    const wrapper = mount(DrivesPage)
    await flushPromises()
    expect(wrapper.text()).toContain('not running as root')
  })

  // While a test runs there is a bar and a way to stop it, and nothing to start.
  it('shows progress and an abort while a test runs', async () => {
    api.diagnostics.drives.mockResolvedValue(
      status({
        drives: [drive({ selfTest: { supported: true, running: true, remaining: 90 } })],
      }),
    )
    const wrapper = mount(DrivesPage)
    await flushPromises()
    expect(wrapper.find('.meter').exists()).toBe(true)
    expect(wrapper.find('.meter-fill').attributes('style')).toContain('width: 10%')
    expect(button(wrapper, 'Abort')).toBeTruthy()
    expect(button(wrapper, 'Short test')).toBeUndefined()
  })

  // A short test starts on the click, and the answer replaces the drive.
  it('starts a short test without asking', async () => {
    const running = drive({ selfTest: { supported: true, running: true, remaining: 90 } })
    api.diagnostics.selfTest.mockResolvedValue(running)
    const wrapper = mount(DrivesPage)
    await flushPromises()

    await button(wrapper, 'Short test').trigger('click')
    await flushPromises()
    expect(api.diagnostics.selfTest).toHaveBeenCalledWith('sda', 'short')
    expect(wrapper.find('.meter').exists()).toBe(true)
    expect(wrapper.text()).toContain('Short test running, 90% left.')
  })

  // The extended test takes hours and slows the drive, so it asks first.
  it('asks before an extended test and does nothing when refused', async () => {
    const confirm = useConfirmStore()
    const ask = vi.spyOn(confirm, 'ask').mockResolvedValue(false)
    const wrapper = mount(DrivesPage)
    await flushPromises()

    await button(wrapper, 'Extended test').trigger('click')
    await flushPromises()
    expect(ask).toHaveBeenCalledTimes(1)
    expect(ask.mock.calls[0][0].question).toContain('sda')
    expect(api.diagnostics.selfTest).not.toHaveBeenCalled()

    ask.mockResolvedValue(true)
    api.diagnostics.selfTest.mockResolvedValue(drive())
    await button(wrapper, 'Extended test').trigger('click')
    await flushPromises()
    expect(api.diagnostics.selfTest).toHaveBeenCalledWith('sda', 'long')
  })

  it('shows what the API refused', async () => {
    api.diagnostics.selfTest.mockRejectedValue(new Error('forbidden'))
    const wrapper = mount(DrivesPage)
    await flushPromises()
    await button(wrapper, 'Short test').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role=alert]').text()).toContain('forbidden')
  })
})
