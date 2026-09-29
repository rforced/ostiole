import { ref } from 'vue'

import { api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import { useToastStore } from '@/stores/toast'

/**
 * The interfaces a wake can go out on, read the way the server's CheckWake
 * reads a configuration: in a zone that is not external, with Ethernet
 * under them, and not a port of a bridge, a bond or a PPPoE session.
 * Interfaces that are off are included; the caller decides about those.
 *
 * @param {object | null | undefined} cfg a configuration, draft or saved
 */
export function wakeInterfaces(cfg) {
  if (!cfg) return []
  const zones = new Set((cfg.zones ?? []).filter((z) => !z.external).map((z) => z.name))
  const ports = new Set()
  for (const i of cfg.interfaces ?? []) {
    for (const m of (i.bridge ?? i.bond)?.members ?? []) ports.add(m)
    if (i.pppoe?.parent) ports.add(i.pppoe.parent)
  }
  return (cfg.interfaces ?? []).filter(
    (i) => !i.wireguard && !i.tailscale && !i.pppoe && !ports.has(i.name) && zones.has(i.zone),
  )
}

/** How a sentence names a device: its description, or its MAC. */
export function deviceName(d) {
  return d?.description || d?.mac || ''
}

/**
 * Sends wakes for a page and keeps what it shows about them: which one is
 * in flight and what went wrong. A wake that went out is a toast. The
 * router cannot tell whether the machine woke, so it says sent.
 */
export function useWake() {
  const toast = useToastStore()
  /** The key of the wake in flight, '*' for all of them, '' for none. */
  const busy = ref('')
  /** @type {import('vue').Ref<string[]>} */
  const errors = ref([])

  /**
   * @param {{interface: string, mac: string}} target
   * @param {string} name how the toast names the machine
   * @param {string} [key] which button spins
   */
  async function wake(target, name, key = target.mac) {
    if (busy.value) return
    busy.value = key
    errors.value = []
    try {
      await api.wol.wake({ interface: target.interface, mac: target.mac })
      toast.show(`Sent a wake packet to ${name}.`)
    } catch (e) {
      errors.value = [`${name}: ${errorMessage(e)}`]
    } finally {
      busy.value = ''
    }
  }

  /** Wakes each device in turn, then says how many went out and which did not. */
  async function wakeAll(devices) {
    if (busy.value) return
    busy.value = '*'
    errors.value = []
    const failed = []
    let sent = 0
    for (const d of devices) {
      try {
        await api.wol.wake({ interface: d.interface, mac: d.mac })
        sent++
      } catch (e) {
        failed.push(`${deviceName(d)}: ${errorMessage(e)}`)
      }
    }
    busy.value = ''
    errors.value = failed
    if (sent)
      toast.show(
        sent === 1 ? 'Sent a wake packet to 1 device.' : `Sent wake packets to ${sent} devices.`,
      )
  }

  return { busy, errors, wake, wakeAll }
}
