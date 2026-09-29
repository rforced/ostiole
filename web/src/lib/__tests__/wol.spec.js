import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { deviceName, useWake, wakeInterfaces } from '@/lib/wol'
import { useToastStore } from '@/stores/toast'

vi.mock('@/lib/api', () => ({ api: { wol: { wake: vi.fn() } } }))

describe('wakeInterfaces', () => {
  // The same reading as the server's CheckWake: inside zones, Ethernet
  // under the link, and never a port of something else.
  it('offers the inside interfaces a wake can go out on', () => {
    const cfg = {
      zones: [{ name: 'lan' }, { name: 'wan', external: true }],
      interfaces: [
        { name: 'eth0', zone: 'wan', enabled: true },
        { name: 'eth1', zone: 'lan', enabled: true },
        { name: 'eth1.20', zone: 'lan', enabled: true, vlan: { parent: 'eth1', id: 20 } },
        { name: 'br0', zone: 'lan', enabled: true, bridge: { members: ['eth2'] } },
        { name: 'eth2', enabled: true },
        { name: 'bond0', zone: 'lan', enabled: true, bond: { members: ['eth3'] } },
        { name: 'eth3', zone: 'lan', enabled: true },
        { name: 'ppp0', zone: 'wan', enabled: true, pppoe: { parent: 'eth4' } },
        { name: 'eth4', zone: 'lan', enabled: true },
        { name: 'wg0', zone: 'lan', enabled: true, wireguard: {} },
        { name: 'tailscale0', zone: 'lan', enabled: true, tailscale: {} },
        { name: 'eth5', zone: 'lan', enabled: false },
        { name: 'eth6' },
      ],
    }
    expect(wakeInterfaces(cfg).map((i) => i.name)).toEqual([
      'eth1',
      'eth1.20',
      'br0',
      'bond0',
      'eth5',
    ])
    expect(wakeInterfaces(null)).toEqual([])
  })

  it('names a device by its description, or its MAC', () => {
    expect(deviceName({ description: 'NAS', mac: 'aa:bb:cc:00:00:01' })).toBe('NAS')
    expect(deviceName({ mac: 'aa:bb:cc:00:00:01' })).toBe('aa:bb:cc:00:00:01')
    expect(deviceName(undefined)).toBe('')
  })
})

describe('useWake', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('says a wake was sent, or why it was not', async () => {
    const toast = useToastStore()
    const { busy, errors, wake } = useWake()
    api.wol.wake.mockResolvedValueOnce(undefined)
    const sending = wake({ interface: 'eth1', mac: 'aa:bb:cc:00:00:01', id: 'x' }, 'NAS', 'x')
    expect(busy.value).toBe('x')
    await sending
    expect(api.wol.wake).toHaveBeenCalledWith({ interface: 'eth1', mac: 'aa:bb:cc:00:00:01' })
    expect(toast.toasts.at(-1).message).toBe('Sent a wake packet to NAS.')
    expect(busy.value).toBe('')

    api.wol.wake.mockRejectedValueOnce(new Error('interface "eth1" is off'))
    await wake({ interface: 'eth1', mac: 'aa:bb:cc:00:00:01' }, 'NAS')
    expect(errors.value).toEqual(['NAS: interface "eth1" is off'])
    expect(toast.toasts).toHaveLength(1)
  })

  it('wakes each device and names the ones that failed', async () => {
    const toast = useToastStore()
    const { errors, wakeAll } = useWake()
    api.wol.wake
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error('sending a wake needs the daemon to run as root'))
      .mockResolvedValueOnce(undefined)
    await wakeAll([
      { interface: 'eth1', mac: 'aa:bb:cc:00:00:01', description: 'NAS' },
      { interface: 'eth1', mac: 'aa:bb:cc:00:00:02' },
      { interface: 'eth1', mac: 'aa:bb:cc:00:00:03', description: 'Desktop' },
    ])
    expect(api.wol.wake).toHaveBeenCalledTimes(3)
    expect(toast.toasts.at(-1).message).toBe('Sent wake packets to 2 devices.')
    expect(errors.value).toEqual([
      'aa:bb:cc:00:00:02: sending a wake needs the daemon to run as root',
    ])
  })
})
