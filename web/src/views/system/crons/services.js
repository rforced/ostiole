/**
 * The services a cron can restart. `label` is the option, `name` how a
 * sentence says it, as the logs page and the DNS pages do.
 */
export const SERVICES = [
  { value: 'dnsmasq', label: 'DHCP and DNS', name: 'DHCP and DNS' },
  { value: 'unbound', label: 'Validating resolver', name: 'the validating resolver' },
  { value: 'chronyd', label: 'Time', name: 'the time service' },
  { value: 'ostiole', label: 'Ostiole', name: 'Ostiole' },
]

/** How a sentence names a service, or its key if it is new. */
export const serviceName = (value) => SERVICES.find((s) => s.value === value)?.name ?? value
