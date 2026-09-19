import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import NetworkDialog from '@/views/wireless/NetworkDialog.vue'
import RadioDialog from '@/views/wireless/RadioDialog.vue'
import WirelessStatus from '@/views/wireless/WirelessStatus.vue'

vi.mock('@/lib/api', () => ({
  api: { wireless: { radios: vi.fn(), clients: vi.fn() } },
  ApiError: class ApiError extends Error {},
}))

const stubs = { ConfirmButton: true, RouterLink: true }

/** The AX210 as the API reports it: one network, no 6 GHz for this test. */
function card(over = {}) {
  return {
    name: 'wlp3s0',
    phy: 'phy0',
    driver: 'iwlwifi',
    selfManaged: true,
    maxNetworks: 1,
    bands: {
      '2g': {
        channels: [
          { number: 1, mhz: 2412 },
          { number: 6, mhz: 2437 },
        ],
        maxWidth: 40,
        standards: ['ax', 'n', 'legacy'],
      },
      '5g': {
        channels: [
          { number: 36, mhz: 5180 },
          { number: 52, mhz: 5260, radar: true },
          { number: 149, mhz: 5745, disabled: true },
        ],
        maxWidth: 160,
        standards: ['ax', 'ac', 'n', 'legacy'],
      },
    },
    ...over,
  }
}

function config(over = {}) {
  return {
    version: 5,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }, { name: 'guest' }],
    interfaces: [
      {
        name: 'br-lan',
        zone: 'lan',
        enabled: true,
        ipv4: { mode: 'static', address: '10.88.0.1/24' },
        bridge: { members: ['enp1s0'] },
      },
    ],
    rules: [],
    wireless: {
      country: 'US',
      radios: [
        { name: 'wlp3s0', enabled: true, band: '5g', channel: 36, width: 80, standard: 'ax' },
      ],
    },
    ...over,
  }
}

describe('WirelessStatus', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  async function strip(radios, draft = config()) {
    api.wireless.radios.mockResolvedValue(radios)
    const store = useConfigStore()
    store.draft = draft
    store.saved = draft
    store.loaded = true
    const wrapper = mount(WirelessStatus, { global: { stubs } })
    await flushPromises()
    return wrapper
  }

  it('says what to run when hostapd is not here', async () => {
    const wrapper = await strip({ setUp: false, radios: [] })
    expect(wrapper.text()).toContain('ostiole repair --wireless')
  })

  it('says so when the router has no card', async () => {
    const wrapper = await strip({ setUp: true, radios: [] })
    expect(wrapper.text()).toBe('No radios on this router.')
  })

  it('asks for a country before anything can transmit', async () => {
    const draft = config({ wireless: { radios: config().wireless.radios } })
    const wrapper = await strip({ setUp: true, radios: [card()] }, draft)
    expect(wrapper.text()).toBe('Set the country on the Radios tab.')
  })

  // The page shows the channel hostapd settled on, not the one it was
  // given: it may swap the pair to match a neighbour.
  it('shows the live channel and the clients on each network', async () => {
    const draft = config()
    draft.interfaces.push({
      name: 'ap0',
      enabled: true,
      ipv4: { mode: 'none' },
      wireless: { radio: 'wlp3s0', ssid: 'ostiole-lan', security: 'wpa2-wpa3', passphrase: 'x' },
    })
    const running = {
      channel: 40,
      width: 80,
      txPower: 22,
      networks: [{ interface: 'ap0', ssid: 'ostiole-lan', clients: 3 }],
    }
    const wrapper = await strip({ setUp: true, country: 'US', radios: [card({ running })] }, draft)
    expect(wrapper.text()).toContain('on air')
    expect(wrapper.text()).toContain('40 · 80 MHz')
    expect(wrapper.text()).toContain('3 clients')
  })

  it('calls a configured radio with no networks off', async () => {
    const wrapper = await strip({ setUp: true, country: 'US', radios: [card()] })
    expect(wrapper.text()).toContain('off')
  })
})

describe('RadioDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  function open(props) {
    const store = useConfigStore()
    store.draft = config()
    store.loaded = true
    return mount(RadioDialog, {
      props: { open: true, ...props },
      global: { stubs: { AppDialog: { template: '<div><slot /></div>' } } },
    })
  }

  // A channel a radar may be on, or one the country forbids, cannot be
  // used here at all, so neither is offered.
  it('offers only the bands the card has and the channels it may use', async () => {
    const wrapper = open({ card: card(), radio: null })
    await flushPromises()
    const bands = wrapper.find('#radio-band').findAll('option')
    expect(bands.map((o) => o.attributes('value'))).toEqual(['2g', '5g'])
    const channels = wrapper.find('#radio-channel').findAll('option')
    expect(channels.map((o) => o.text())).toEqual(['Automatic', '36 · 5180 MHz'])
    // 160 MHz on 5 GHz would need radar channels, so it is not offered
    // even on a card that does it.
    const widths = wrapper.find('#radio-width').findAll('option')
    expect(widths.map((o) => o.text())).toEqual(['20 MHz', '40 MHz', '80 MHz'])
  })

  // The card has the band; the rules let nothing be started on it.
  it('leaves out a band the card may not transmit on', async () => {
    const six = {
      channels: [{ number: 1, mhz: 5955 }],
      maxWidth: 160,
      standards: ['ax'],
      serves: false,
    }
    const wrapper = open({ card: card({ bands: { ...card().bands, '6g': six } }), radio: null })
    await flushPromises()
    const bands = wrapper.find('#radio-band').findAll('option')
    expect(bands.map((o) => o.attributes('value'))).toEqual(['2g', '5g'])
  })

  it('lands a width it no longer offers on the widest it does', async () => {
    const radio = { ...config().wireless.radios[0], width: 160 }
    const wrapper = open({ card: card(), radio })
    await flushPromises()
    expect(wrapper.find('#radio-width').element.value).toBe('80')
  })

  it('saves a radio into the draft', async () => {
    const wrapper = open({ card: card(), radio: null })
    await flushPromises()
    await wrapper.find('form').trigger('submit')
    const store = useConfigStore()
    expect(store.radios.at(-1)).toMatchObject({ name: 'wlp3s0', band: '5g', width: 80 })
  })
})

describe('NetworkDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  function open(draft = config()) {
    const store = useConfigStore()
    store.draft = draft
    store.loaded = true
    return mount(NetworkDialog, {
      props: { open: true, network: null },
      global: { stubs: { AppDialog: { template: '<div><slot /></div>' } } },
    })
  }

  it('names the first free interface and puts the network on a bridge', async () => {
    const wrapper = open()
    await flushPromises()
    expect(wrapper.find('#net-name').element.value).toBe('ap0')

    await wrapper.find('#net-ssid').setValue('ostiole-lan')
    await wrapper.find('#net-pass').setValue('correct horse battery')
    await wrapper.find('form').trigger('submit')

    const store = useConfigStore()
    const net = store.findInterface('ap0')
    expect(net.wireless).toMatchObject({ radio: 'wlp3s0', ssid: 'ostiole-lan' })
    expect(net.zone).toBe('')
    // The bridge is what carries the segment, so the network joins it.
    expect(store.findInterface('br-lan').bridge.members).toContain('ap0')
  })

  it('gives a network in a zone an address of its own', async () => {
    const wrapper = open()
    await flushPromises()
    await wrapper.find('#net-ssid').setValue('ostiole-guest')
    await wrapper.find('#net-pass').setValue('correct horse battery')
    await wrapper.findAll('input[type="radio"]')[1].setValue()
    await flushPromises()
    await wrapper.find('#net-address').setValue('10.99.0.1/24')
    await wrapper.find('form').trigger('submit')

    const store = useConfigStore()
    const net = store.findInterface('ap0')
    expect(net.ipv4).toEqual({ mode: 'static', address: '10.99.0.1/24' })
    expect(store.findInterface('br-lan').bridge.members).not.toContain('ap0')
  })
})
