const ALPHABET = 'abcdefghijklmnopqrstuvwxyz0123456789'

/**
 * Short random identifier for rules and NAT entries, matching the server's
 * id pattern (letter or digit first, then letters, digits, '_', '.', '-').
 * @param {string} prefix
 */
export function newId(prefix) {
  const bytes = new Uint8Array(6)
  crypto.getRandomValues(bytes)
  let out = ''
  for (const b of bytes) out += ALPHABET[b % ALPHABET.length]
  return `${prefix}-${out}`
}
