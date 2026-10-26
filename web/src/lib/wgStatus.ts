/**
 * How long a peer counts as connected after its last handshake. Its
 * session keys are refused past three minutes, so it shakes hands again
 * before it sends anything; the log calls it quiet from then on.
 */
export const QUIET_AFTER_MS = 3 * 60 * 1000

/**
 * A peer as its tunnel's device reports it, found by its key.
 * @param live the tunnels by name
 */
export function livePeer<P extends { publicKey: string }>(
  live: Map<string, { peers: P[] | null }>,
  tunnel: string,
  publicKey: string,
) {
  return live.get(tunnel)?.peers?.find((q) => q.publicKey === publicKey)
}

/**
 * Whether a peer shook hands recently enough to be sending.
 * @param q the peer as its device reports it
 */
export function connected(q: { lastHandshake?: string } | undefined, now = Date.now()) {
  if (!q?.lastHandshake) return false
  return now - new Date(q.lastHandshake).getTime() < QUIET_AFTER_MS
}
