/**
 * The pages inside the sections that have more than one. The router turns
 * these into child routes and the sidebar renders them as sub-items, so their
 * order, their labels and the components behind them are written once.
 *
 * @typedef {{path: string, label: string, view: () => Promise<unknown>}} SectionPage
 */

/** @type {SectionPage[]} */
export const FIREWALL_PAGES = [
  { path: 'rules', label: 'Rules', view: () => import('@/views/firewall/RulesTab.vue') },
  { path: 'aliases', label: 'Aliases', view: () => import('@/views/firewall/AliasesTab.vue') },
  {
    path: 'schedules',
    label: 'Schedules',
    view: () => import('@/views/firewall/SchedulesTab.vue'),
  },
  { path: 'nat', label: 'NAT', view: () => import('@/views/firewall/NatTab.vue') },
  { path: 'log', label: 'Log', view: () => import('@/views/firewall/LogTab.vue') },
]

/** @type {SectionPage[]} */
export const SERVICE_PAGES = [
  { path: 'dhcp', label: 'DHCP', view: () => import('@/views/services/DhcpTab.vue') },
  { path: 'dhcp6', label: 'DHCPv6', view: () => import('@/views/services/Dhcp6Tab.vue') },
  { path: 'dns', label: 'DNS', view: () => import('@/views/services/DnsTab.vue') },
  { path: 'leases', label: 'Leases', view: () => import('@/views/services/LeasesTab.vue') },
]
