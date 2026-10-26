import type { HostOverride } from '@/lib/types.gen'

/** The key the diff uses for an override: the label, with its domain when it has one. */
export function overrideKey(h: Pick<HostOverride, 'hostname' | 'domain'>) {
  return h.domain ? `${h.hostname}.${h.domain}` : h.hostname
}

/** The name in full, under the local domain when the override has none. */
export function overrideName(h: Pick<HostOverride, 'hostname' | 'domain'>, local?: string) {
  const domain = h.domain || local
  return domain ? `${h.hostname}.${domain}` : h.hostname
}
