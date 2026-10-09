import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useConfigStore } from '@/stores/config'
import TunnelFileDialog from '@/views/vpn/TunnelFileDialog.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const mod = await importOriginal()
  return { ...mod, api: { ...mod.api, wireguard: { publicKey: vi.fn() } } }
})

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

// The wg(8) man page's example key pair, and a public key of the same shape.
const PRIVATE = 'yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk='
const PUBLIC = 'HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw='
const SERVER = 'xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg='

const PROVIDER = `[Interface]
PrivateKey = ${PRIVATE}
Address = 10.64.1.2/32, fc00:bbbb:bbbb:bb01::1:2/128
DNS = 10.64.0.1
PostUp = iptables -A FORWARD -i %i -j ACCEPT

[Peer]
PublicKey = ${SERVER}
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 198.51.100.7:51820
`

async function open() {
  const config = useConfigStore()
  config.replaceDraft({
    version: 12,
    zones: [{ name: 'wan', external: true }, { name: 'lan' }],
    interfaces: [
      { name: 'eth0', zone: 'wan', enabled: true, ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } },
      {
        name: 'wg0',
        zone: 'lan',
        enabled: true,
        ipv4: { mode: 'static', address: '10.66.0.1/24' },
        ipv6: { mode: 'none' },
        wireguard: { privateKey: 'x', peers: [] },
      },
    ],
    rules: [],
  })
  const wrapper = mount(TunnelFileDialog, { props: { open: false }, global: { stubs } })
  await wrapper.setProps({ open: true })
  return { wrapper, config }
}

describe('TunnelFileDialog', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.wireguard.publicKey.mockResolvedValue({ publicKey: PUBLIC })
  })

  it('adds a provider as a way out, with a gateway watching its DNS server', async () => {
    const { wrapper, config } = await open()
    await wrapper.find('#tf-text').setValue(PROVIDER)
    expect(wrapper.find('#tf-name').element.value).toBe('wg1')
    expect(wrapper.text()).toContain('Ignored: PostUp on line 5.')
    expect(wrapper.text()).toContain('wg1, new and external')
    expect(wrapper.find('#tf-monitor').element.value).toBe('10.64.0.1')

    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(api.wireguard.publicKey).toHaveBeenCalledWith(PRIVATE)
    expect(config.zones.find((z) => z.name === 'wg1')).toEqual({ name: 'wg1', external: true })
    expect(config.interfaces.find((i) => i.name === 'wg1')).toEqual({
      name: 'wg1',
      enabled: true,
      zone: 'wg1',
      ipv4: { mode: 'static', address: '10.64.1.2/32' },
      ipv6: { mode: 'static', address: 'fc00:bbbb:bbbb:bb01::1:2/128' },
      wireguard: {
        privateKey: PRIVATE,
        publicKey: PUBLIC,
        peers: [
          {
            name: 'server',
            enabled: true,
            publicKey: SERVER,
            allowedIps: ['0.0.0.0/0', '::/0'],
            endpoint: '198.51.100.7:51820',
          },
        ],
      },
    })
    expect(config.gateways).toEqual([
      { name: 'wg1', enabled: true, interface: 'wg1', monitor: '10.64.0.1' },
    ])
  })

  it('names the peer and the tunnel after the file chosen', async () => {
    const { wrapper, config } = await open()
    const input = wrapper.find('input[type="file"]')
    const file = new File([PROVIDER], 'se-got-wg-001.conf', { type: 'text/plain' })
    Object.defineProperty(input.element, 'files', { value: [file] })
    await input.trigger('change')
    await flushPromises()
    expect(wrapper.find('#tf-desc').element.value).toBe('se-got-wg-001')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    const wg1 = config.interfaces.find((i) => i.name === 'wg1')
    expect(wg1.description).toBe('se-got-wg-001')
    expect(wg1.wireguard.peers[0].name).toBe('se_got_wg_001')
  })

  it('says where a file goes wrong and saves nothing', async () => {
    const { wrapper, config } = await open()
    await wrapper.find('#tf-text').setValue(`[Interface]
PrivateKey = ${PRIVATE}
Address = 10.64.1.2/32, 10.64.1.3/32
`)
    const alert = wrapper.find('[role="alert"]')
    expect(alert.text()).toContain(
      'Line 3: more than one IPv4 address. A tunnel here holds one of each.',
    )
    expect(alert.text()).toContain('There is no [Peer] section.')
    expect(wrapper.find('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(config.interfaces).toHaveLength(2)
  })

  it('refuses a name another interface has', async () => {
    const { wrapper, config } = await open()
    await wrapper.find('#tf-text').setValue(PROVIDER)
    await wrapper.find('#tf-name').setValue('wg0')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toBe(
      'wg0 is taken. Give the tunnel another name.',
    )
    expect(config.gateways ?? []).toEqual([])
  })
})
