import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import ModemPage from '@/views/diagnostics/ModemPage.vue'

vi.mock('@/lib/api', () => ({
  api: { diagnostics: { modem: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

function status() {
  return {
    address: '192.168.100.1',
    vendor: 'Hitron',
    model: 'CODA',
    firmware: '7.3.5.3.2b1',
    link: { up: true, speed: '2500Mbps', duplex: 'Full' },
    provisioning: [
      { name: 'Hardware', status: 'Success', ok: true },
      { name: 'Ranging', status: 'Success', ok: true },
      { name: 'DHCP', status: 'In progress', ok: false },
      { name: 'Registration', status: 'Not started', ok: false },
    ],
    downstream: [
      {
        channel: 1,
        kind: 'qam',
        frequency: 447000000,
        modulation: 'QAM256',
        locked: true,
        power: 5.7,
        snr: 40.4,
        octets: 1,
        corrected: 0,
        uncorrectable: 0,
      },
      {
        channel: 2,
        kind: 'ofdm',
        frequency: 659600000,
        modulation: 'OFDM 4K',
        locked: true,
        power: -8.5,
        snr: 31,
        octets: 1,
        corrected: 75915354,
        uncorrectable: 12,
      },
    ],
    upstream: [
      {
        channel: 10,
        kind: 'qam',
        frequency: 29200000,
        bandwidth: 6400000,
        modulation: '64QAM',
        mode: 'ATDMA',
        power: 41.0,
      },
      {
        channel: 9,
        kind: 'qam',
        frequency: 35600000,
        bandwidth: 6400000,
        modulation: '64QAM',
        mode: 'ATDMA',
        power: 52.5,
      },
    ],
    fetchedAt: '2026-09-20T05:00:00Z',
  }
}

describe('ModemPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    api.diagnostics.modem.mockResolvedValue(status())
  })

  // Opening the page reads the minute-old copy; the button asks the modem again.
  it('reads the cached status on open and the modem itself on Read', async () => {
    const wrapper = mount(ModemPage)
    await flushPromises()
    expect(api.diagnostics.modem).toHaveBeenCalledWith('192.168.100.1', false)
    expect(wrapper.text()).toContain('Hitron CODA')

    await wrapper.find('button').trigger('click')
    await flushPromises()
    expect(api.diagnostics.modem).toHaveBeenLastCalledWith('192.168.100.1', true)
  })

  // The first step that failed is where the WAN stopped; the levels get
  // the cable industry's thresholds so a bad channel stands out.
  it('names the step provisioning stopped at and grades the levels', async () => {
    const wrapper = mount(ModemPage)
    await flushPromises()
    expect(wrapper.text()).toContain('Stopped at DHCP')

    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].text()).toContain('447 MHz')
    expect(rows[0].find('.badge-ok').exists()).toBe(true)
    // -8.5 dBmV is a look, 31 dB SNR is a look, and 12 uncorrectable is red.
    expect(rows[1].text()).toContain('OFDM')
    expect(rows[1].findAll('.badge-warn')).toHaveLength(2)
    expect(rows[1].find('.text-bad').text()).toBe('12')
    // 52.5 dBmV upstream is past the 51 that is still just a look.
    expect(rows[3].find('.badge-bad').exists()).toBe(true)
  })

  // An address other than the DOCSIS default is remembered in this browser.
  it('remembers a different address', async () => {
    const wrapper = mount(ModemPage)
    await flushPromises()
    await wrapper.find('input').setValue('10.0.0.1')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(api.diagnostics.modem).toHaveBeenLastCalledWith('10.0.0.1', true)
    expect(localStorage.getItem('ostiole.modem.address')).toBe('10.0.0.1')

    const again = mount(ModemPage)
    await flushPromises()
    expect(again.find('input').element.value).toBe('10.0.0.1')
    expect(api.diagnostics.modem).toHaveBeenLastCalledWith('10.0.0.1', false)
  })

  it('shows the error when nothing answers', async () => {
    api.diagnostics.modem.mockRejectedValue(new Error('no known modem answered at 192.168.100.1'))
    const wrapper = mount(ModemPage)
    await flushPromises()
    expect(wrapper.find('[role=alert]').text()).toContain('no known modem answered')
    expect(wrapper.find('table').exists()).toBe(false)
  })
})
