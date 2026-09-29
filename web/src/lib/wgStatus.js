/**
 * How long a peer counts as connected after its last handshake. Its
 * session keys are refused past three minutes, so it shakes hands again
 * before it sends anything; the log calls it quiet from then on.
 */
export const QUIET_AFTER_MS = 3 * 60 * 1000

/**
 * A peer as its tunnel's device reports it, found by its key.
 * @param {Map<string, {peers: Array<{publicKey: string}>}>} live the tunnels by name
 * @param {string} tunnel
 * @param {string} publicKey
 */
export function livePeer(live, tunnel, publicKey) {
  return live.get(tunnel)?.peers.find((q) => q.publicKey === publicKey)
}

/**
 * Whether a peer shook hands recently enough to be sending.
 * @param {{lastHandshake?: string} | undefined} q the peer as its device reports it
 * @param {number} [now]
 */
export function connected(q, now = Date.now()) {
  if (!q?.lastHandshake) return false
  return now - new Date(q.lastHandshake).getTime() < QUIET_AFTER_MS
}
