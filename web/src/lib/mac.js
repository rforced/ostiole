/**
 * Prefixes that set the locally administered bit on an address that does
 * not change: libvirt's for its guests and Docker's for its containers.
 */
const FIXED = ['52:54:00', '02:42']

/**
 * Whether a device made its MAC up rather than using the one its maker
 * burned in, as phones and laptops do for a private address per network:
 * the locally administered bit.
 * @param {string | null | undefined} mac
 */
export function isRandomMAC(mac) {
  const m = (mac ?? '').toLowerCase()
  if (!/^[0-9a-f]{2}(:[0-9a-f]{2}){5}$/.test(m)) return false
  if (FIXED.some((p) => m.startsWith(p))) return false
  return (parseInt(m.slice(0, 2), 16) & 2) !== 0
}
