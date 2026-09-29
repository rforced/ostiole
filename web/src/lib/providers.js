/** How two spellings of one name are compared: model.NormalizeDomain. */
const canonical = (s) =>
  String(s ?? '')
    .trim()
    .replace(/^\.+|\.+$/g, '')
    .toLowerCase()

/**
 * The DNS provider holding the longest domain that is the name or above
 * it, and that domain: `model.ProviderFor` on the server, held to the same
 * cases (internal/model/testdata/provider-for.json). A wildcard belongs
 * where its parent does.
 *
 * @param {Array<{id: string, domains?: string[]}>} providers
 * @param {string} name
 * @returns {{provider: {id: string}, zone: string} | null}
 */
export function providerFor(providers, name) {
  const n = canonical(
    String(name ?? '')
      .trim()
      .replace(/^\*\./, ''),
  )
  let found = null
  let zone = ''
  for (const p of providers ?? []) {
    for (const raw of p.domains ?? []) {
      const d = canonical(raw)
      if (!d || d.length <= zone.length) continue
      if (n === d || n.endsWith(`.${d}`)) {
        found = p
        zone = d
      }
    }
  }
  return found ? { provider: found, zone } : null
}

/**
 * The provider a dns-01 certificate is written through, as
 * `model.CertificateProvider` finds it: the one it names, or else the one
 * holding the domain of every name it covers. With none, problem says why,
 * or is empty when there are no names yet.
 *
 * @param {Array<{id: string, domains?: string[]}>} providers
 * @param {{provider?: string, names?: string[]}} cert
 * @returns {{provider: {id: string} | null, problem: string}}
 */
export function certificateProvider(providers, cert) {
  if (cert.provider) {
    const named = (providers ?? []).find((p) => p.id === cert.provider)
    return named
      ? { provider: named, problem: '' }
      : { provider: null, problem: `Unknown DNS provider ${cert.provider}.` }
  }
  let found = null
  let first = ''
  for (const raw of cert.names ?? []) {
    const name = String(raw).trim()
    if (!name) continue
    const hit = providerFor(providers, name)
    if (!hit) return { provider: null, problem: `No DNS provider holds ${name}.` }
    if (!found) {
      found = hit.provider
      first = name
    } else if (found.id !== hit.provider.id) {
      return {
        provider: null,
        problem: `${first} is on ${found.id} and ${name} on ${hit.provider.id}; name the provider that writes both.`,
      }
    }
  }
  return { provider: found, problem: '' }
}
