import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'

import { appendPoint, deviceLabel, sinceLine, useTrafficStream, type Point } from '@/lib/traffic'

let latest: FakeSource | undefined
const source = () => {
  if (!latest) throw new Error('no stream was opened')
  return latest
}
class FakeSource {
  static CLOSED = 2
  url: string
  closed = false
  readyState = 1
  onopen: () => void = () => {}
  onmessage: (m: { data: string }) => void = () => {}
  onerror: () => void = () => {}
  constructor(url: string) {
    this.url = url
    latest = this
  }
  close() {
    this.closed = true
  }
}

describe('traffic helpers', () => {
  it('lets go of points older than the window as one is added', () => {
    const pts: Point[] = [
      [100, 1, 1],
      [300, 2, 2],
    ]
    expect(appendPoint(pts, [401, 3, 3], '5m')).toEqual([
      [300, 2, 2],
      [401, 3, 3],
    ])
    expect(pts).toHaveLength(2)
  })

  it('names the router and a device known only by its address', () => {
    expect(deviceLabel({ router: true, id: 'router' })).toBe('This router')
    expect(deviceLabel({ id: '10.0.0.5' })).toBe('10.0.0.5')
    expect(deviceLabel({ id: 'aa', mac: 'aa:bb', name: 'laptop' })).toBe('laptop')
  })

  it('says since when only where the window reaches back further', () => {
    const now = Date.parse('2026-09-27T12:00:00Z') / 1000
    expect(sinceLine('2026-09-27T11:59:00Z', '5m', now)).toMatch(/^Since .+\.$/)
    expect(sinceLine('2026-09-27T11:50:00Z', '5m', now)).toBe('')
    expect(sinceLine('', '24h', now)).toBe('')
  })
})

describe('useTrafficStream', () => {
  beforeEach(() => vi.stubGlobal('EventSource', FakeSource))

  function harness(on: Parameters<typeof useTrafficStream>[0]) {
    let out: ReturnType<typeof useTrafficStream> | undefined
    const w = mount(
      defineComponent({
        setup() {
          out = useTrafficStream(on)
          return () => h('div')
        },
      }),
    )
    return {
      w,
      out: () => {
        if (!out) throw new Error('the stream was not set up')
        return out
      },
    }
  }

  // A stream that comes back after a drop means the page reads afresh:
  // the router may have restarted, with counts of its own.
  it('hands events on by kind, and reads again when it reopens', () => {
    const links = vi.fn()
    const devices = vi.fn()
    const reopened = vi.fn()
    const { w, out } = harness({ links, devices, reopened })
    expect(source().url).toBe('/api/v1/traffic/stream')
    source().onopen()
    source().onmessage({ data: JSON.stringify({ kind: 'links', links: [] }) })
    source().onmessage({ data: JSON.stringify({ kind: 'devices', devices: [] }) })
    source().onmessage({ data: 'garbage' })
    expect(links).toHaveBeenCalledTimes(1)
    expect(devices).toHaveBeenCalledTimes(1)
    expect(reopened).not.toHaveBeenCalled()
    source().readyState = 0
    source().onerror()
    expect(out().error.value).toBe('Stream disconnected, retrying…')
    source().onopen()
    expect(reopened).toHaveBeenCalledTimes(1)
    expect(out().error.value).toBe('')
    w.unmount()
    expect(source().closed).toBe(true)
  })
})
