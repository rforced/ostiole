import { onBeforeUnmount, onMounted, ref } from 'vue'

import { SPANS } from '@/lib/chart'
import { streamLost } from '@/lib/stream'

/** The windows the Destinations tab offers. */
export const DESTINATION_WINDOWS = [
  { value: '1h', label: '1 hour' },
  { value: '24h', label: '24 hours' },
  { value: '7d', label: '7 days' },
]

/** The Destinations window a Devices window reads a device's from. */
export const DESTINATIONS_FOR = { '5m': '1h', '24h': '24h', '30d': '7d' }

/**
 * What a destination row's port is: its service where the router names
 * one, else the port and protocol, or the protocol alone.
 * @param {{service?: string, port?: number, protocol: string}} r
 */
export function serviceLabel(r) {
  if (r.service) return r.service
  return r.port ? `${r.port}/${r.protocol}` : r.protocol
}

/** The windows the Devices and Interfaces tabs offer, shortest first. */
export const WINDOWS = [
  { value: '5m', label: '5 minutes' },
  { value: '24h', label: '24 hours' },
  { value: '30d', label: '30 days' },
]

/**
 * The traffic stream while the page is shown: every link's rate each
 * second, every device's after each read of the connection table. It
 * closes while the page is hidden. When it opens again, after the page
 * was hidden or the router came back, reopened reads the window afresh:
 * points from before would not line up with the ones that follow.
 *
 * @param {{links?: (ev: object) => void, devices?: (ev: object) => void, reopened?: () => void}} on
 */
export function useTrafficStream({ links, devices, reopened }) {
  const error = ref('')
  let source = null
  let opened = false

  function open() {
    const es = new EventSource('/api/v1/traffic/stream')
    source = es
    es.onopen = () => {
      error.value = ''
      if (opened) reopened?.()
      opened = true
    }
    es.onmessage = (m) => {
      let ev
      try {
        ev = JSON.parse(m.data)
      } catch {
        return
      }
      if (ev.kind === 'links') links?.(ev)
      else if (ev.kind === 'devices') devices?.(ev)
    }
    es.onerror = () => {
      error.value = streamLost(es)
    }
  }

  function close() {
    source?.close()
    source = null
  }

  function visibility() {
    if (document.visibilityState === 'hidden') close()
    else if (!source) open()
  }

  onMounted(() => {
    open()
    document.addEventListener('visibilitychange', visibility)
  })
  onBeforeUnmount(() => {
    close()
    document.removeEventListener('visibilitychange', visibility)
  })
  return { error }
}

/**
 * A series with a point added and those older than the window let go.
 * @param {Array<[number, number, number]>} points
 * @param {[number, number, number]} point
 * @param {string} window a key of SPANS
 */
export function appendPoint(points, point, window) {
  const cut = point[0] - SPANS[window]
  let i = 0
  while (i < points.length && points[i][0] < cut) i++
  return [...points.slice(i), point]
}

/** What the page calls a device: its name, This router, or how it is known. */
export function deviceLabel(d) {
  if (d.router) return 'This router'
  return d.name || d.mac || d.id
}

/**
 * When counting began, where the window reaches back further: "Since
 * 14:02." over a day, the date as well over a month. Empty otherwise.
 * @param {string} since
 * @param {string} window
 * @param {number} now seconds
 */
export function sinceLine(since, window, now) {
  if (!since) return ''
  const t = Date.parse(since)
  if (!Number.isFinite(t) || t / 1000 <= now - SPANS[window]) return ''
  const d = new Date(t)
  const when =
    window === '30d'
      ? d.toLocaleString(undefined, {
          day: 'numeric',
          month: 'short',
          hour: '2-digit',
          minute: '2-digit',
        })
      : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  return `Since ${when}.`
}
