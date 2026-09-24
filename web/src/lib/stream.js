import { api } from '@/lib/api'

/**
 * Says why a log stream stopped. The browser reconnects a dropped stream on
 * its own and gives up on one the server refused, which is what a stream
 * whose session has ended gets. Asking who is signed in then sends a 401 to
 * the login page the way any other request would.
 * @param {EventSource} source
 * @returns {string}
 */
export function streamLost(source) {
  if (source.readyState !== EventSource.CLOSED) return 'Stream disconnected, retrying…'
  api.auth.me().catch(() => {})
  return 'Stream stopped. Reload the page to reconnect.'
}
