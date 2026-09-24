import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { streamLost } from '@/lib/stream'

vi.mock('@/lib/api', () => ({ api: { auth: { me: vi.fn() } } }))

describe('streamLost', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('EventSource', { CONNECTING: 0, OPEN: 1, CLOSED: 2 })
    api.auth.me.mockResolvedValue({ username: 'admin' })
  })

  it('leaves a dropped stream to the browser, which reconnects it', () => {
    expect(streamLost({ readyState: 0 })).toBe('Stream disconnected, retrying…')
    expect(api.auth.me).not.toHaveBeenCalled()
  })

  // A session that ended while the stream ran is refused on reconnect, and
  // the browser stops there. The look at the session is what turns that
  // 401 into the login page.
  it('asks after the session when the server refused the stream', () => {
    expect(streamLost({ readyState: 2 })).toBe('Stream stopped. Reload the page to reconnect.')
    expect(api.auth.me).toHaveBeenCalledTimes(1)
  })
})
