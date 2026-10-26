import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useConfigStore } from '@/stores/config'
import { configOf, must } from '@/test/fixtures'
import OvpnFileDialog from '@/views/vpn/openvpn/OvpnFileDialog.vue'

// The dialog itself teleports; the form inside it is what is under test.
const stubs = {
  AppDialog: {
    props: ['open', 'title', 'description'],
    template: '<div><slot /><slot name="footer" /></div>',
  },
}

const pem = (kind: string, body: string) =>
  `-----BEGIN ${kind}-----\n${body}\n-----END ${kind}-----`
const CA = pem('CERTIFICATE', 'TUFERS1VUC1DQS1mb3ItYS10ZXN0')
const CERT = pem('CERTIFICATE', 'TUFERS1VUC1jbGllbnQtY2VydA==')
const KEY = pem('PRIVATE KEY', 'TUFERS1VUC1jbGllbnQta2V5')

const PROVIDER = `client
dev tun
remote vpn.example 1194 udp
nobind
auth-user-pass
redirect-gateway def1
<ca>
${CA}
</ca>
`

const SITE = `client
remote lab-server.vpn.example 1194
route 10.20.0.0 255.255.0.0
<ca>
${CA}
</ca>
<cert>
${CERT}
</cert>
<key>
${KEY}
</key>
`

async function open(props: { blocksOnly?: boolean } = {}) {
  const config = useConfigStore()
  config.replaceDraft(
    configOf({
      version: 12,
      zones: [{ name: 'wan', external: true }, { name: 'lan' }],
      interfaces: [
        {
          name: 'eth0',
          zone: 'wan',
          enabled: true,
          ipv4: { mode: 'dhcp' },
          ipv6: { mode: 'none' },
        },
      ],
      rules: [],
    }),
  )
  const wrapper = mount(OvpnFileDialog, { props: { open: false, ...props }, global: { stubs } })
  await wrapper.setProps({ open: true })
  return { wrapper, config }
}

describe('OvpnFileDialog', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('adds a provider as a way out, with its zone, credentials and a gateway', async () => {
    const { wrapper, config } = await open()
    await wrapper.find('#ov-text').setValue(PROVIDER)
    expect(wrapper.find<HTMLInputElement>('#ov-name').element.value).toBe('ovpn0')
    expect(wrapper.find<HTMLInputElement>('#ov-wayout').element.checked).toBe(true)
    expect(wrapper.text()).toContain('Ignored: nobind on line 4.')
    await wrapper.find('#ov-username').setValue('acme')
    await wrapper.find('#ov-password').setValue('not-a-real-password')
    await wrapper.find('#ov-monitor').setValue('10.8.0.1')

    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(config.zones.find((z) => z.name === 'ovpn0')).toEqual({ name: 'ovpn0', external: true })
    expect(config.interfaces.find((i) => i.name === 'ovpn0')).toEqual({
      name: 'ovpn0',
      enabled: true,
      zone: 'ovpn0',
      ipv4: { mode: 'none' },
      ipv6: { mode: 'none' },
      openvpn: {
        remotes: [{ host: 'vpn.example', port: 1194 }],
        caPem: `${CA}\n`,
        username: 'acme',
        password: 'not-a-real-password',
      },
    })
    expect(config.gateways).toEqual([
      { name: 'ovpn0', enabled: true, interface: 'ovpn0', monitor: '10.8.0.1' },
    ])
    expect(must(wrapper.emitted('update:open')).at(-1)).toEqual([false])
  })

  it('adds a site tunnel with no gateway when the monitor is empty', async () => {
    const { wrapper, config } = await open()
    await wrapper.find('#ov-text').setValue(SITE)
    expect(wrapper.find('#ov-username').exists()).toBe(false)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(config.zones.find((z) => z.name === 'ovpn0')).toEqual({ name: 'ovpn0' })
    expect(must(config.interfaces.find((i) => i.name === 'ovpn0')).openvpn).toEqual({
      remotes: [{ host: 'lab-server.vpn.example', port: 1194 }],
      caPem: `${CA}\n`,
      certPem: `${CERT}\n`,
      keyPem: `${KEY}\n`,
      routes: ['10.20.0.0/16'],
    })
    expect(config.gateways ?? []).toEqual([])
  })

  it('lists what it refuses by line and saves nothing', async () => {
    const { wrapper, config } = await open()
    await wrapper.find('#ov-text').setValue(`up /usr/local/bin/thing\n${PROVIDER}comp-lzo\n`)
    const alert = wrapper.find('[role="alert"]')
    expect(alert.text()).toContain('Line 1: up runs a program on this router.')
    expect(alert.text()).toContain('Line 13: comp-lzo compresses traffic')
    expect(alert.text()).not.toContain('/usr/local/bin/thing')
    expect(wrapper.find('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(config.interfaces).toHaveLength(1)
  })

  it('hands back only the blocks when replacing them', async () => {
    const { wrapper, config } = await open({ blocksOnly: true })
    await wrapper
      .find('#ov-text')
      .setValue(`<ca>\n${CA}\n</ca>\n<cert>\n${CERT}\n</cert>\n<key>\n${KEY}\n</key>\n`)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.find('form').trigger('submit')
    expect(must(must(wrapper.emitted('blocks'))[0])[0]).toEqual({
      caPem: `${CA}\n`,
      certPem: `${CERT}\n`,
      keyPem: `${KEY}\n`,
    })
    expect(config.interfaces).toHaveLength(1)
  })
})
