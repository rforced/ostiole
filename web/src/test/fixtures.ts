import type { Session } from '@/lib/api'
import type { Config, Interface, Rule } from '@/lib/types.gen'

/** A model value that holds only the parts a test reads. */
export type DeepPartial<T> = {
  [K in keyof T]?: T[K] extends (infer U)[] | null
    ? DeepPartial<U>[]
    : T[K] extends object | null
      ? DeepPartial<NonNullable<T[K]>>
      : T[K]
}

/** A model value with only the parts a test cares about. */
export const fixture = <T>(partial: DeepPartial<T>) => partial as T

/** A configuration with only the parts a test cares about. */
export const configOf = (partial: DeepPartial<Config>) => fixture<Config>(partial)

/** A signed-in account with only the parts a test cares about. */
export const sessionOf = (partial: Omit<Partial<Session>, 'role'> & { role?: string }) =>
  partial as Session

/** A configuration with only the sections Go always sends, for a spec to build on. */
export function emptyConfig(): Config {
  return {
    version: 11,
    system: { management: { webPort: 8443, sshPort: 22, sshPasswords: false } },
    zones: [],
    interfaces: [],
    rules: [],
    nat: { outbound: { mode: 'automatic' } },
    services: { dhcp: { enabled: false }, dns: { enabled: false }, upnp: { enabled: false } },
    blocking: { enforce: {} },
    updates: { system: {}, ostiole: {} },
  }
}

/** An interface with the fields Go always sends left empty, as a fixture that omits them reads. */
export function iface(over: Partial<Interface> & { name: string }): Interface {
  return { enabled: false, ipv4: { mode: '' }, ipv6: { mode: '' }, ...over }
}

/** A rule with the fields Go always sends left empty. */
export function rule(over: Partial<Rule> & { id: string }): Rule {
  return {
    enabled: false,
    zone: '',
    action: '',
    protocol: '',
    source: {},
    destination: {},
    ...over,
  }
}
