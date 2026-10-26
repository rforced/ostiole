import { ref } from 'vue'

import { api } from '@/lib/api'
import { errorMessage } from '@/lib/async'
import { segmentInterfaces } from '@/lib/interfaces'
import type { Config, WakeRequest } from '@/lib/types.gen'
import { useToastStore } from '@/stores/toast'

/**
 * The interfaces a wake can go out on.
 * @param cfg a configuration, draft or saved
 */
export function wakeInterfaces(cfg: Config | null | undefined) {
  return segmentInterfaces(cfg)
}

/** How a sentence names a device: its description, or its MAC. */
export function deviceName(d?: { description?: string; mac?: string } | null): string {
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
  const errors = ref<string[]>([])

  /**
   * @param name how the toast names the machine
   * @param key which button spins
   */
  async function wake(target: WakeRequest, name: string, key = target.mac) {
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
  async function wakeAll(devices: Array<WakeRequest & { description?: string }>) {
    if (busy.value) return
    busy.value = '*'
    errors.value = []
    const failed: string[] = []
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
